package main

import "testing"

func TestTemplatePlanHeadline(t *testing.T) {
	p := templatePlan(studioParams{Prompt: "A 12 second ad for a small coffee roaster: fresh beans, morning light, order online", Duration: 12})
	if got := p.Shots[0].Text; got != "A small coffee roaster" {
		t.Fatalf("headline %q", got)
	}
	if p.Shots[0].Sub == "" || p.Shots[len(p.Shots)-1].Type != "card" {
		t.Fatalf("plan %+v", p.Shots)
	}
	total := 0.0
	for _, s := range p.Shots {
		total += s.Seconds
	}
	if total < 11 || total > 13 {
		t.Fatalf("total %v", total)
	}
}

func TestParsePlanFromModel(t *testing.T) {
	out := "Sure! ```json\n{\"title\":\"Beans\",\"palette\":[\"#111111\",\"#eeeeee\",\"bad\"],\"shots\":[{\"type\":\"card\",\"seconds\":3,\"text\":\"Fresh beans\"},{\"type\":\"AI\",\"seconds\":4,\"prompt\":\"coffee beans pouring, morning light\",\"narration\":\"Roasted this week.\"},{\"type\":\"ai\",\"seconds\":3},{\"type\":\"video\"}]}\n```"
	p := parsePlan(out, 10)
	// The prompt-less AI shot has nothing to show and is dropped; "video" isn't a shot type.
	if p == nil || len(p.Shots) != 2 || p.Shots[1].Type != "ai" || p.Palette[2] != "#4cc398" {
		t.Fatalf("plan %+v", p)
	}
}
