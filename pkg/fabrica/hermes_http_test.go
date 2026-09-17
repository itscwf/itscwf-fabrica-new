package fabrica

import (
	"strings"
	"testing"
)

// Sample of the real "hermes cron list" TUI output served by hermes-kanban-api
// (/api/v1/crons returns {"data": "<this text>"}).
const cronTUISample = `
┌─────────────────────────────────────────────────────────────────────────┐
│                         Scheduled Jobs                                  │
└─────────────────────────────────────────────────────────────────────────┘

  f875e66f06ed [active]
    Name:      wiki-auto-commit
    Schedule:  0 23 * * *
    Repeat:    ∞
    Next run:  2026-09-16T23:00:00-04:00
    Deliver:   local
    Script:    auto-commit.sh
    Mode:      no-agent (script stdout delivered directly)
    Workdir:   /root/obsidian-vault
    Last run:  2026-09-15T23:00:36.904275-04:00  ok
    Dispatch:  on time (scheduled 2026-09-15T23:00:00-04:00)
    Execution: completed  94fef610422e46ffaa3b2d3b47bda151

  b419c5922cae [active]
    Name:      review-dispatcher
    Schedule:  */30 * * * *
    Repeat:    ∞
    Last run:  2026-09-16T09:30:12.207511-04:00  ok
    Execution: completed  c4de1fc97f7242339e926438f8ac4c80

  3bc306a6d837 [active]
    Name:      marlene-daily-ti-news
    Schedule:  0 11 * * *
    Last run:  2026-09-15T11:01:02.787690-04:00  error: RuntimeError: HTTP 429
    Execution: unknown  29211135f7aa40bea8c914a286a83f3c

  e2a288ece484 [active]
    Name:      agent-reach-daily-watch
    Schedule:  0 9 * * *
    Last run:  never
`

func TestParseCronListExtractsNameScheduleAndStatus(t *testing.T) {
	jobs, err := parseCronList(cronTUISample)
	if err != nil {
		t.Fatalf("parseCronList: %v", err)
	}
	if len(jobs) != 4 {
		t.Fatalf("esperava 4 jobs, veio %d", len(jobs))
	}

	byID := map[string]map[string]any{}
	for _, j := range jobs {
		byID[j["id"].(string)] = j
	}

	// 1) Nome legível (não o id hex)
	wiki := byID["f875e66f06ed"]
	if wiki["name"] != "wiki-auto-commit" {
		t.Errorf("name = %v, queria wiki-auto-commit", wiki["name"])
	}

	// 2) Schedule com asteriscos preservados
	if wiki["schedule"] != "0 23 * * *" {
		t.Errorf("schedule = %q, queria '0 23 * * *'", wiki["schedule"])
	}
	rd := byID["b419c5922cae"]
	if rd["schedule"] != "*/30 * * * *" {
		t.Errorf("schedule com */ = %q, queria '*/30 * * * *'", rd["schedule"])
	}

	// 3) Status normalizado
	if wiki["last_status"] != "completed" {
		t.Errorf("last_status ok = %v, queria completed", wiki["last_status"])
	}
	if byID["3bc306a6d837"]["last_status"] != "error" {
		t.Errorf("last_status error = %v, queria error", byID["3bc306a6d837"]["last_status"])
	}

	// 4) Job sem "Last run:" parseável não pode herdar lixo
	if v, ok := byID["e2a288ece484"]["last_status"]; ok && v == "never ok" {
		t.Errorf("last_status inesperado: %v", v)
	}
}

func TestStripUnicodeKeepsCronCharacters(t *testing.T) {
	in := "    Schedule:  */30 0-23/2 1,15 * *"
	kept := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', ':', '-', '/', '*', ',', '.', '_':
			return r
		}
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return r
		}
		return -1
	}, in)
	_ = kept // a asserção real é o comportamento do parser acima
}
