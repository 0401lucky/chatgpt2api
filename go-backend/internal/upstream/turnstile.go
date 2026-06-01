package upstream

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

type turnstileFunc func(args ...any)

type orderedMap struct {
	keys   []string
	values map[string]any
}

func newOrderedMap() *orderedMap {
	return &orderedMap{values: map[string]any{}}
}

func (m *orderedMap) add(key string, value any) {
	if _, ok := m.values[key]; !ok {
		m.keys = append(m.keys, key)
	}
	m.values[key] = value
}

func solveTurnstileToken(dx, p string) string {
	token, _ := solveTurnstileTokenWithStatus(dx, p)
	return token
}

func solveTurnstileTokenWithStatus(dx, p string) (string, string) {
	if strings.TrimSpace(dx) == "" {
		return "", "missing_dx"
	}
	decoded, err := base64.StdEncoding.DecodeString(dx)
	if err != nil {
		return "", "base64_decode_failed"
	}
	var tokenList []any
	if err := json.Unmarshal([]byte(xorString(string(decoded), p)), &tokenList); err != nil {
		return "", "json_unmarshal_failed"
	}

	processMap := map[string]any{}
	pendingPrograms := [][]any{tokenList}
	start := time.Now()
	result := ""
	resultCalls := 0
	resultNonStringCalls := 0

	processMap["1"] = turnstileFunc(func(args ...any) {
		if len(args) < 2 {
			return
		}
		e, t := turnstileKey(args[0]), turnstileKey(args[1])
		processMap[e] = xorString(turnstileToString(processMap[e]), turnstileToString(processMap[t]))
	})
	processMap["2"] = turnstileFunc(func(args ...any) {
		if len(args) < 2 {
			return
		}
		processMap[turnstileKey(args[0])] = args[1]
	})
	processMap["3"] = turnstileFunc(func(args ...any) {
		resultCalls++
		if len(args) < 1 {
			return
		}
		text, ok := args[0].(string)
		if !ok {
			resultNonStringCalls++
			return
		}
		result = base64.StdEncoding.EncodeToString([]byte(text))
	})
	processMap["5"] = turnstileFunc(func(args ...any) {
		if len(args) < 2 {
			return
		}
		e, t := turnstileKey(args[0]), turnstileKey(args[1])
		current := processMap[e]
		incoming := processMap[t]
		if list, ok := current.([]any); ok {
			processMap[e] = append(list, incoming)
			return
		}
		if _, ok := current.(string); ok {
			processMap[e] = turnstileToString(current) + turnstileToString(incoming)
			return
		}
		if _, ok := current.(float64); ok {
			processMap[e] = turnstileToString(current) + turnstileToString(incoming)
			return
		}
		if _, ok := incoming.(string); ok {
			processMap[e] = turnstileToString(current) + turnstileToString(incoming)
			return
		}
		if _, ok := incoming.(float64); ok {
			processMap[e] = turnstileToString(current) + turnstileToString(incoming)
			return
		}
		processMap[e] = "NaN"
	})
	processMap["6"] = turnstileFunc(func(args ...any) {
		if len(args) < 3 {
			return
		}
		e, t, n := turnstileKey(args[0]), turnstileKey(args[1]), turnstileKey(args[2])
		tv, tok := processMap[t].(string)
		nv, nok := processMap[n].(string)
		if !tok || !nok {
			return
		}
		value := tv + "." + nv
		if value == "window.document.location" {
			processMap[e] = "https://chatgpt.com/"
			return
		}
		processMap[e] = value
	})
	processMap["7"] = turnstileFunc(func(args ...any) {
		if len(args) < 1 {
			return
		}
		target := processMap[turnstileKey(args[0])]
		values := make([]any, 0, len(args)-1)
		for _, arg := range args[1:] {
			values = append(values, processMap[turnstileKey(arg)])
		}
		if target == "window.Reflect.set" && len(values) >= 3 {
			if object, ok := values[0].(*orderedMap); ok {
				object.add(turnstileToString(values[1]), values[2])
			}
			return
		}
		callTurnstileCallable(target, values)
	})
	processMap["8"] = turnstileFunc(func(args ...any) {
		if len(args) < 2 {
			return
		}
		processMap[turnstileKey(args[0])] = processMap[turnstileKey(args[1])]
	})
	processMap["14"] = turnstileFunc(func(args ...any) {
		if len(args) < 2 {
			return
		}
		key := turnstileKey(args[0])
		var value any
		if json.Unmarshal([]byte(turnstileToString(processMap[turnstileKey(args[1])])), &value) == nil {
			processMap[key] = value
			if key == "9" {
				if program, ok := turnstileProgram(value); ok && len(program) > 0 {
					pendingPrograms = append(pendingPrograms, program)
				}
			}
		}
	})
	processMap["15"] = turnstileFunc(func(args ...any) {
		if len(args) < 2 {
			return
		}
		data, _ := json.Marshal(processMap[turnstileKey(args[1])])
		processMap[turnstileKey(args[0])] = string(data)
	})
	processMap["17"] = turnstileFunc(func(args ...any) {
		if len(args) < 2 {
			return
		}
		e, t := turnstileKey(args[0]), turnstileKey(args[1])
		target := processMap[t]
		callArgs := make([]any, 0, len(args)-2)
		for _, arg := range args[2:] {
			callArgs = append(callArgs, processMap[turnstileKey(arg)])
		}
		switch target {
		case "window.performance.now":
			processMap[e] = float64(time.Since(start).Nanoseconds())/1e6 + rand.Float64()/1e6
		case "window.Object.create":
			processMap[e] = newOrderedMap()
		case "window.Object.keys":
			if len(callArgs) > 0 && callArgs[0] == "window.localStorage" {
				processMap[e] = []any{
					"STATSIG_LOCAL_STORAGE_INTERNAL_STORE_V4",
					"STATSIG_LOCAL_STORAGE_STABLE_ID",
					"client-correlated-secret",
					"oai/apps/capExpiresAt",
					"oai-did",
					"STATSIG_LOCAL_STORAGE_LOGGING_REQUEST",
					"UiState.isNavigationCollapsed.1",
				}
			}
		case "window.Math.random":
			processMap[e] = rand.Float64()
		default:
			if isTurnstileCallable(target) {
				callTurnstileCallable(target, callArgs)
				processMap[e] = nil
			}
		}
	})
	processMap["18"] = turnstileFunc(func(args ...any) {
		if len(args) < 1 {
			return
		}
		key := turnstileKey(args[0])
		decoded, err := base64.StdEncoding.DecodeString(turnstileToString(processMap[key]))
		if err == nil {
			processMap[key] = string(decoded)
		}
	})
	processMap["19"] = turnstileFunc(func(args ...any) {
		if len(args) < 1 {
			return
		}
		key := turnstileKey(args[0])
		processMap[key] = base64.StdEncoding.EncodeToString([]byte(turnstileToString(processMap[key])))
	})
	processMap["20"] = turnstileFunc(func(args ...any) {
		if len(args) < 3 {
			return
		}
		if !turnstileEqual(processMap[turnstileKey(args[0])], processMap[turnstileKey(args[1])]) {
			return
		}
		target := processMap[turnstileKey(args[2])]
		values := make([]any, 0, len(args)-3)
		for _, arg := range args[3:] {
			values = append(values, processMap[turnstileKey(arg)])
		}
		callTurnstileCallable(target, values)
	})
	processMap["21"] = turnstileFunc(func(args ...any) {})
	processMap["23"] = turnstileFunc(func(args ...any) {
		if len(args) < 2 {
			return
		}
		if processMap[turnstileKey(args[0])] == nil {
			return
		}
		callTurnstileCallable(processMap[turnstileKey(args[1])], args[2:])
	})
	processMap["24"] = turnstileFunc(func(args ...any) {
		if len(args) < 3 {
			return
		}
		e, t, n := turnstileKey(args[0]), turnstileKey(args[1]), turnstileKey(args[2])
		tv, tok := processMap[t].(string)
		nv, nok := processMap[n].(string)
		if tok && nok {
			processMap[e] = tv + "." + nv
		}
	})

	processMap["9"] = tokenList
	processMap["10"] = "window"
	processMap["16"] = p

	missingOps := map[string]int{}
	executedOps := map[string]int{}
	programCount := 0
	for len(pendingPrograms) > 0 {
		if programCount >= 8 {
			return "", fmt.Sprintf("program_limit_exceeded:programs=%d;ops=%s;missing=%s", programCount, turnstileCountsSummary(executedOps), turnstileCountsSummary(missingOps))
		}
		program := pendingPrograms[0]
		pendingPrograms = pendingPrograms[1:]
		programCount++
		for _, rawToken := range program {
			token, ok := rawToken.([]any)
			if !ok || len(token) == 0 {
				continue
			}
			func() {
				defer func() { _ = recover() }()
				op := turnstileKey(token[0])
				executedOps[op]++
				if fn, ok := processMap[op].(turnstileFunc); ok {
					fn(token[1:]...)
					return
				}
				missingOps[op]++
			}()
		}
	}
	if result == "" {
		return "", fmt.Sprintf("empty_result:programs=%d,result_calls=%d,result_non_string=%d;ops=%s;missing=%s", programCount, resultCalls, resultNonStringCalls, turnstileCountsSummary(executedOps), turnstileCountsSummary(missingOps))
	}
	return result, "ok"
}

func turnstileProgram(value any) ([]any, bool) {
	switch v := value.(type) {
	case []any:
		return v, true
	default:
		return nil, false
	}
}

func turnstileOpSummary(tokenList []any) string {
	counts := map[string]int{}
	for _, rawToken := range tokenList {
		token, ok := rawToken.([]any)
		if !ok || len(token) == 0 {
			continue
		}
		counts[turnstileKey(token[0])]++
	}
	return turnstileCountsSummary(counts)
}

func turnstileCountsSummary(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s:%d", key, counts[key]))
	}
	return strings.Join(parts, ",")
}

func isTurnstileCallable(value any) bool {
	_, ok := value.(turnstileFunc)
	return ok
}

func callTurnstileCallable(target any, args []any) {
	defer func() { _ = recover() }()
	if fn, ok := target.(turnstileFunc); ok {
		fn(args...)
	}
}

func turnstileKey(value any) string {
	switch v := value.(type) {
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case json.Number:
		return v.String()
	case string:
		return v
	default:
		return fmt.Sprint(value)
	}
}

func turnstileToString(value any) string {
	if value == nil {
		return "undefined"
	}
	switch v := value.(type) {
	case string:
		switch v {
		case "window.Math":
			return "[object Math]"
		case "window.Reflect":
			return "[object Reflect]"
		case "window.performance":
			return "[object Performance]"
		case "window.localStorage":
			return "[object Storage]"
		case "window.Object":
			return "function Object() { [native code] }"
		case "window.Reflect.set":
			return "function set() { [native code] }"
		case "window.performance.now":
			return "function () { [native code] }"
		case "window.Object.create":
			return "function create() { [native code] }"
		case "window.Object.keys":
			return "function keys() { [native code] }"
		case "window.Math.random":
			return "function random() { [native code] }"
		default:
			return v
		}
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case []string:
		return strings.Join(v, ",")
	case []any:
		parts := make([]string, 0, len(v))
		allString := true
		for _, item := range v {
			text, ok := item.(string)
			if !ok {
				allString = false
				break
			}
			parts = append(parts, text)
		}
		if allString {
			return strings.Join(parts, ",")
		}
		return fmt.Sprint(value)
	case bool:
		if v {
			return "True"
		}
		return "False"
	case *orderedMap:
		return "[object Object]"
	default:
		return fmt.Sprint(value)
	}
}

func turnstileEqual(left, right any) bool {
	switch l := left.(type) {
	case string:
		return l == turnstileToString(right)
	case float64:
		if r, ok := right.(float64); ok {
			return l == r
		}
		return turnstileToString(left) == turnstileToString(right)
	default:
		return reflect.DeepEqual(left, right)
	}
}

func xorString(text, key string) string {
	if key == "" {
		return text
	}
	out := make([]rune, 0, len(text))
	keyRunes := []rune(key)
	for i, ch := range []rune(text) {
		out = append(out, ch^keyRunes[i%len(keyRunes)])
	}
	return string(out)
}
