package main

import (
	"fmt"
	"strconv"
	"strings"
)

// aiGuide is the prompt a person pastes into their own AI (Claude, ChatGPT, Codex, Cursor and
// the like) so it can use Potluck's model directly. It is written from live state, so the
// addresses, model names and context size in it are the real ones. The same text is served at
// /potluck/ai on the chat address, which the AI can read again later for fresh details.
func (h *Host) aiGuide(owner, fromShare bool) string {
	st := h.chatState(owner)
	models := h.visibleModels(fromShare)
	base := "http://127.0.0.1:" + strconv.Itoa(portChat)
	shared := ""
	if h.cfg.Get().ShareChat {
		if u := h.shareURLs(); len(u) > 0 {
			shared = u[0]
		}
	}

	now := "No model is loaded right now."
	if m, ok := st["model"].(map[string]any); ok {
		title, _ := m["title"].(string)
		switch st["phase"] {
		case "running":
			now = fmt.Sprintf("Loaded now: %s.", title)
		case "resting":
			now = fmt.Sprintf("Loaded now: %s, resting to free memory. Your first request loads it again (up to a minute or two).", title)
		case "starting":
			now = fmt.Sprintf("Loading now: %s.", title)
		default:
			now = fmt.Sprintf("Last model: %s (%v).", title, st["phase"])
		}
	}
	ctx, _ := st["ctx"].(int)

	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...); b.WriteByte('\n') }

	w("# Potluck: live instructions")
	w("Potluck is my private AI. It runs on my own computer(s), not online. Use it when I ask for it or for the local AI, and when it helps: for anything private I would rather not send to an online service, for bulk or repetitive work, or as a second opinion. Tell me when you hand work to it. These details are current as of now; read this address again later for fresh ones.")
	w("")
	w("## Where it is")
	w("- On this computer: %s (OpenAI-compatible API at %s/v1). No API key; if a tool insists on one, use any text such as \"potluck\".", base, base)
	if shared != "" {
		w("- From other devices on my home network or Tailscale: %s/v1, with the header Authorization: Bearer <the key shown in Potluck under Running>.", shared)
	} else {
		w("- It only answers on this computer (127.0.0.1). If you run somewhere else and cannot reach it, say so instead of guessing.")
	}
	w("- %s", now)
	if ctx > 0 {
		w("- Context size: %d tokens (prompt and answer together). Split bigger inputs.", ctx)
	}
	if len(models) > 0 {
		w("- Models on my computers:")
		for _, m := range models {
			mark := ""
			if m.Running {
				mark = " (loaded)"
			}
			w("  - %s%s · file %s · node %s (%s) · %.1f GB", m.Title, mark, m.File, m.Node, m.NodeName, float64(m.Size)/1e9)
		}
	}
	w("- Up-to-date version of these notes: GET %s/potluck/ai", base)
	w("")
	w("## How to use it")
	w("Ask it something (any OpenAI client works the same way; \"model\" can be any text):")
	w("```")
	w("curl -s %s/v1/chat/completions -H 'Content-Type: application/json' --max-time 600 -d '{", base)
	w("  \"model\": \"potluck\",")
	w("  \"messages\": [{\"role\": \"user\", \"content\": \"Summarise this in 3 bullets: ...\"}],")
	w("  \"thinking\": \"off\"")
	w("}'")
	w("```")
	w("- The answer is in choices[0].message.content. If the model reasons first, its reasoning is in choices[0].message.reasoning_content.")
	w("- \"thinking\" is Potluck's one switch for reasoning first: \"off\", \"low\", \"medium\" or \"high\". Potluck turns it into what the loaded model understands. Use \"high\" for hard questions; it is slower.")
	w("- Add \"stream\": true to stream the answer as server-sent events.")
	w("- It accepts OpenAI-style \"tools\" for function calling, if the loaded model supports them.")
	w("- Python: openai.OpenAI(base_url=\"%s/v1\", api_key=\"potluck\").chat.completions.create(model=\"potluck\", messages=[...])", base)
	w("- Allow a long timeout (10 minutes). The first request after a rest loads the model before it answers.")
	w("")
	w("Check what is running: GET %s/potluck/state. \"phase\" is running, resting (wakes on your next request), starting (wait and check again every 5 s), idle (nothing loaded) or error.", base)
	w("List the models on my computers: GET %s/potluck/models", base)
	w("Switch models (only from this computer): POST %s/potluck/switch with header \"X-Potluck: 1\" and body {\"file\": \"<file>\", \"node\": \"<node>\", \"ctx\": 16384}. Then wait for phase \"running\".", base)
	w("")
	w("## Ground rules")
	w("- Ask me before switching models: it stops the model that is loaded now, and loading takes a while.")
	w("- One request at a time works best; it runs on home hardware, not a data centre.")
	w("- It is a smaller model than you. Check its answers before relying on them, and do not pass it secrets it does not need.")
	w("- Only use the addresses listed here. Do not change Potluck's settings; the control panel is for me.")
	w("- If I ask for a \"better\" model: bigger files are usually smarter but slower. Tell me the choices and ask before switching.")
	return b.String()
}

// aiMemory is what the control panel copies: a short note the person's own AI saves to its
// memory (or turns into a skill), so that "use Potluck" or "run this on the local AI" means
// something to it later. It holds only what stays true; the AI reads the live details from
// /potluck/ai when it needs them.
func aiMemory() string {
	base := "http://127.0.0.1:" + strconv.Itoa(portChat)
	return `Please save this to your long-term memory (or as a skill named "potluck", if you can make skills), so you know it in every future conversation. Then confirm in one line what you saved.

---
Potluck is my private AI. It runs on my own computer(s), not online.
When I say "use Potluck", "ask Potluck", "run this on the local AI", "use my local model" or "my own AI", or I want something kept off online services:
1. Read Potluck's live instructions: GET ` + base + `/potluck/ai (plain text). They say which model is loaded, how to send it work (an OpenAI-compatible API at ` + base + `/v1, no key needed) and the rules. Follow them.
2. Send the work to Potluck and tell me the answer came from it. Check its answers; it is a smaller model than you. If an answer is poor, ask Potluck again or tell me.
3. If nothing answers at that address, Potluck is not running: ask me to open it. When I asked for the local AI, never send the work to an online model instead.
This works only from this computer (` + base + `). If you can't make web requests or run commands here, tell me so.
---`
}
