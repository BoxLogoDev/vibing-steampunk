package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/oisee/vibing-steampunk/pkg/adt"
	"github.com/spf13/cobra"
)

var sqlCmd = &cobra.Command{
	Use:   "sql [statement]",
	Short: "Run read-only freestyle SQL, JSON out (--stdin for JSONL batch)",
	Long: `Run ABAP SQL through the ADT data preview, always in read-only mode.

Single:  vsp sql "SELECT bukrs, butxt FROM t001" --max 50
Batch:   vsp sql --stdin   < requests.jsonl   (one session for all queries)
         request  {"id":"t001","sql":"SELECT ...","max":1000}
         response {"id":"t001","ok":true,"cols":[...],"rows":[[...]],"n":9,"ms":120,"tries":1}

Lines are folded to <=200 chars outside string literals: the server cuts every
SQL line at 255 chars without an error. "No further temporary subroutine pools"
is retried after 5s/15s/60s.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runSQL,
}

var (
	queryMax   int
	queryStdin bool
)

func init() {
	sqlCmd.Flags().IntVar(&queryMax, "max", 100, "Max rows (single mode, and batch default)")
	sqlCmd.Flags().BoolVar(&queryStdin, "stdin", false, "Read JSONL requests from stdin, write JSONL responses")
	rootCmd.AddCommand(sqlCmd)
}

type queryReq struct {
	ID  string `json:"id"`
	SQL string `json:"sql"`
	Max int    `json:"max"`
}

type queryResp struct {
	ID    string          `json:"id"`
	OK    bool            `json:"ok"`
	Cols  []string        `json:"cols,omitempty"`
	Rows  [][]interface{} `json:"rows,omitempty"`
	N     int             `json:"n"`
	Ms    int64           `json:"ms"`
	Tries int             `json:"tries"`
	Err   string          `json:"err,omitempty"`
}

// foldSQL breaks lines at the first space outside a string literal once a line
// reaches width. ABAP SQL escapes a quote as '', which toggles twice, so a plain
// toggle tracks literal state correctly.
func foldSQL(sql string, width int) string {
	var b strings.Builder
	col, inStr := 0, false
	for _, r := range sql {
		if r == '\n' {
			b.WriteRune(r)
			col = 0
			continue
		}
		if r == '\'' {
			inStr = !inStr
		}
		if r == ' ' && !inStr && col >= width {
			b.WriteRune('\n')
			col = 0
			continue
		}
		b.WriteRune(r)
		col++
	}
	return b.String()
}

var queryRetryDelays = []time.Duration{5 * time.Second, 15 * time.Second, 60 * time.Second}

func runOneQuery(ctx context.Context, client *adt.Client, req queryReq) queryResp {
	resp := queryResp{ID: req.ID}
	sql := foldSQL(strings.TrimSpace(req.SQL), 200)
	for _, line := range strings.Split(sql, "\n") {
		if utf8.RuneCountInString(line) > 255 {
			resp.Err = "line longer than 255 chars with no foldable space (server would truncate it)"
			return resp
		}
	}
	max := req.Max
	if max <= 0 {
		max = queryMax
	}
	start := time.Now()
	for {
		resp.Tries++
		res, err := client.RunQuery(ctx, sql, max)
		if err == nil {
			resp.OK = true
			resp.Cols = make([]string, len(res.Columns))
			for i, c := range res.Columns {
				resp.Cols[i] = c.Name
			}
			resp.Rows = make([][]interface{}, len(res.Rows))
			for i, row := range res.Rows {
				vals := make([]interface{}, len(resp.Cols))
				for j, name := range resp.Cols {
					vals[j] = row[name]
				}
				resp.Rows[i] = vals
			}
			resp.N = len(resp.Rows)
			break
		}
		if resp.Tries > len(queryRetryDelays) || !strings.Contains(err.Error(), "subroutine pool") {
			resp.Err = err.Error()
			break
		}
		time.Sleep(queryRetryDelays[resp.Tries-1])
	}
	resp.Ms = time.Since(start).Milliseconds()
	return resp
}

func runSQL(cmd *cobra.Command, args []string) error {
	resolveConfig(cmd.Parent())
	if err := validateConfig(); err != nil {
		return err
	}
	if err := processCookieAuth(cmd.Parent()); err != nil {
		return err
	}
	client := createADTClient(adt.WithReadOnly())
	ctx := context.Background()

	if !queryStdin {
		if len(args) != 1 {
			return fmt.Errorf("give one SQL argument, or use --stdin")
		}
		resp := runOneQuery(ctx, client, queryReq{ID: "1", SQL: args[0], Max: queryMax})
		out, _ := json.Marshal(resp)
		fmt.Println(string(out))
		if !resp.OK {
			return fmt.Errorf("query failed")
		}
		return nil
	}

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	w := bufio.NewWriter(os.Stdout)
	for in.Scan() {
		line := strings.TrimSpace(in.Text())
		if line == "" {
			continue
		}
		var req queryReq
		var resp queryResp
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			resp = queryResp{Err: "bad request json: " + err.Error()}
		} else {
			resp = runOneQuery(ctx, client, req)
		}
		out, _ := json.Marshal(resp)
		w.Write(out)
		w.WriteByte('\n')
		w.Flush()
	}
	return in.Err()
}
