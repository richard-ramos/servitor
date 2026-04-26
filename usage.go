package main

import (
	"bufio"
	"encoding/json"
	"os"
)

func ParseUsageFromJSONL(path string) UsageRecord {
	f, err := os.Open(path)
	if err != nil {
		return UsageRecord{}
	}
	defer f.Close()
	var best UsageRecord
	var rawBest string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		var obj any
		if json.Unmarshal(line, &obj) != nil {
			continue
		}
		u := usageFromValue(obj)
		if u.Model == "" {
			u.Model = findStringKey(obj, "model")
		}
		score := u.TotalTokens
		if score == 0 {
			score = u.InputTokens + u.OutputTokens
		}
		bestScore := best.TotalTokens
		if bestScore == 0 {
			bestScore = best.InputTokens + best.OutputTokens
		}
		if score > bestScore || (score == bestScore && (u.Model != "" || u.InputTokens != 0 || u.OutputTokens != 0)) {
			best = u
			rawBest = string(line)
		}
	}
	best.RawJSON = rawBest
	return best
}

func usageFromValue(v any) UsageRecord {
	var best UsageRecord
	walkUsage(v, &best)
	if best.TotalTokens == 0 && (best.InputTokens != 0 || best.OutputTokens != 0) {
		best.TotalTokens = best.InputTokens + best.OutputTokens
	}
	return best
}

func walkUsage(v any, best *UsageRecord) {
	m, ok := v.(map[string]any)
	if !ok {
		if arr, ok := v.([]any); ok {
			for _, item := range arr {
				walkUsage(item, best)
			}
		}
		return
	}
	if usage, ok := m["usage"].(map[string]any); ok {
		candidate := UsageRecord{
			Model:        stringFromAny(m["model"]),
			InputTokens:  tokenField(usage, "input_tokens", "prompt_tokens"),
			OutputTokens: tokenField(usage, "output_tokens", "completion_tokens"),
			TotalTokens:  tokenField(usage, "total_tokens"),
		}
		score := candidate.TotalTokens
		if score == 0 {
			score = candidate.InputTokens + candidate.OutputTokens
		}
		bestScore := best.TotalTokens
		if bestScore == 0 {
			bestScore = best.InputTokens + best.OutputTokens
		}
		if score >= bestScore {
			*best = candidate
		}
	}
	for _, child := range m {
		walkUsage(child, best)
	}
}

func tokenField(m map[string]any, keys ...string) int64 {
	for _, key := range keys {
		if n := intFromAny(m[key]); n != 0 {
			return n
		}
	}
	return 0
}

func findStringKey(v any, key string) string {
	switch x := v.(type) {
	case map[string]any:
		if s := stringFromAny(x[key]); s != "" {
			return s
		}
		for _, child := range x {
			if s := findStringKey(child, key); s != "" {
				return s
			}
		}
	case []any:
		for _, child := range x {
			if s := findStringKey(child, key); s != "" {
				return s
			}
		}
	}
	return ""
}

func intFromAny(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int64:
		return x
	case int:
		return int64(x)
	case json.Number:
		n, _ := x.Int64()
		return n
	default:
		return 0
	}
}

func stringFromAny(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
