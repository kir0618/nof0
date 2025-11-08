package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"nof0-api/pkg/backtest"
	executorpkg "nof0-api/pkg/executor"
	"nof0-api/pkg/journal"
	"nof0-api/pkg/llm"
)

func main() {
	var (
		journalDir    = flag.String("journal-dir", "journal", "Path to journal directory")
		limit         = flag.Int("limit", 5, "Number of recent cycles to replay")
		executorPath  = flag.String("executor-config", "etc/executor.yaml", "Executor config file")
		templatePath  = flag.String("template", "etc/prompts/executor/default_prompt.tmpl", "Executor prompt template")
		modelAlias    = flag.String("model", "journal-replay", "Model alias for replay executor")
		replaySymbol  = flag.String("replay-symbol", "", "Optional symbol for backtest replay (requires journal market data)")
		initialEquity = flag.Float64("initial-equity", 100000, "Initial equity for journal backtest replay")
	)
	flag.Parse()

	reader := journal.NewReader(*journalDir)
	records, err := reader.Latest(*limit)
	if err != nil {
		log.Fatalf("load journal: %v", err)
	}
	if len(records) == 0 {
		log.Println("no journal cycles found")
		return
	}

	execCfg, err := executorpkg.LoadConfig(*executorPath)
	if err != nil {
		log.Fatalf("load executor config: %v", err)
	}

	stub := &replayLLM{}
	executor, err := executorpkg.NewExecutor(execCfg, stub, *templatePath, *modelAlias)
	if err != nil {
		log.Fatalf("init executor: %v", err)
	}

	ctx := context.Background()
	passed := 0
	failed := 0
	for idx, rec := range records {
		label := fmt.Sprintf("%s #%d", rec.TraderID, idx+1)
		stub.SetPayload(rec.DecisionsJSON)
		execCtx := journal.BuildExecutorContext(execCfg, rec)
		result, err := executor.GetFullDecision(&execCtx)
		if err != nil {
			failed++
			log.Printf("[FAIL] %s executor validation: %v", label, err)
			continue
		}
		if err := compareDecisions(rec.DecisionsJSON, result.Decisions); err != nil {
			failed++
			log.Printf("[FAIL] %s decision mismatch: %v", label, err)
			continue
		}
		passed++
		log.Printf("[OK]   %s cycles decisions=%d success=%t", label, len(result.Decisions), rec.Success)
	}

	log.Printf("journal replay complete: %d passed, %d failed", passed, failed)
	if failed > 0 {
		os.Exit(1)
	}

	if sym := strings.ToUpper(strings.TrimSpace(*replaySymbol)); sym != "" {
		res, err := backtest.RunJournalReplay(ctx, records, sym, *initialEquity)
		if err != nil {
			log.Fatalf("backtest replay: %v", err)
		}
		log.Printf("backtest replay %s: trades=%d win_rate=%.2f%% total_pnl=%.2f", sym, res.Trades, res.WinRate*100, res.TotalPNL)
	}
}

func compareDecisions(recordedJSON string, replayed []executorpkg.Decision) error {
	recorded, err := journal.ParseDecisionsJSON(recordedJSON)
	if err != nil {
		return fmt.Errorf("parse recorded decisions: %w", err)
	}
	if len(recorded) == 0 && len(replayed) == 0 {
		return nil
	}
	sameLen := len(recorded) == len(replayed)
	if !sameLen {
		return fmt.Errorf("decision count mismatch recorded=%d replayed=%d", len(recorded), len(replayed))
	}
	normRecorded, err := normalizeDecisions(recorded)
	if err != nil {
		return err
	}
	normReplay, err := normalizeDecisions(replayed)
	if err != nil {
		return err
	}
	if normRecorded != normReplay {
		return fmt.Errorf("decision payload mismatch")
	}
	return nil
}

func normalizeDecisions(decisions []executorpkg.Decision) (string, error) {
	type compact struct {
		Symbol          string  `json:"symbol"`
		Action          string  `json:"action"`
		PositionSizeUSD float64 `json:"position_size_usd"`
		EntryPrice      float64 `json:"entry_price"`
		StopLoss        float64 `json:"stop_loss"`
		TakeProfit      float64 `json:"take_profit"`
		Confidence      int     `json:"confidence"`
	}
	comp := make([]compact, 0, len(decisions))
	for _, d := range decisions {
		comp = append(comp, compact{
			Symbol:          strings.ToUpper(strings.TrimSpace(d.Symbol)),
			Action:          strings.ToLower(strings.TrimSpace(d.Action)),
			PositionSizeUSD: d.PositionSizeUSD,
			EntryPrice:      d.EntryPrice,
			StopLoss:        d.StopLoss,
			TakeProfit:      d.TakeProfit,
			Confidence:      d.Confidence,
		})
	}
	data, err := json.Marshal(comp)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// replayLLM feeds recorded decisions back into the executor without hitting an LLM.
type replayLLM struct {
	payload string
}

func (r *replayLLM) SetPayload(payload string) { r.payload = payload }

func (r *replayLLM) Chat(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	return nil, errors.New("replay client does not support Chat")
}

func (r *replayLLM) ChatStream(ctx context.Context, req *llm.ChatRequest) (<-chan llm.StreamResponse, error) {
	return nil, errors.New("replay client does not support streaming")
}

func (r *replayLLM) ChatStructured(ctx context.Context, req *llm.ChatRequest, target interface{}) (*llm.ChatResponse, error) {
	if strings.TrimSpace(r.payload) == "" {
		return &llm.ChatResponse{Model: req.Model}, nil
	}
	if err := llm.ParseStructured(r.payload, target); err != nil {
		return nil, err
	}
	return &llm.ChatResponse{Model: req.Model, Usage: llm.Usage{}}, nil
}

func (r *replayLLM) GetConfig() *llm.Config { return nil }

func (r *replayLLM) Close() error { return nil }
