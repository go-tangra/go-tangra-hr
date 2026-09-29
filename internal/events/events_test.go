package events

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-hr/v4/internal/stream"
)

const tn = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"

var bg = context.Background()

func TestEmitter(t *testing.T) {
	rec := &Recorder{}
	em := Emitter{Pub: rec}
	em.RequestChanged(bg, tn, "r1", "approved", []string{"maria", "petar", "maria", ""})
	em.ReviewChanged(bg, tn, []string{"petar"})
	em.ReviewChanged(bg, tn, nil) // nobody: dropped
	em.CalendarChanged(bg, tn, time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC))
	got := rec.Events()
	if len(got) != 3 || got[0].Type != RequestChanged || len(got[0].Users) != 2 || got[1].Type != ReviewChanged || got[2].Users != nil {
		t.Fatalf("events: %+v", got)
	}
	raw, _ := json.Marshal(got[2].Payload)
	if string(raw) != `{"from":"2026-08-03","to":"2026-08-07"}` {
		t.Fatalf("calendar payload: %s", raw)
	}
	raw, _ = json.Marshal(got[0].Payload)
	if string(raw) != `{"request_id":"r1","status":"approved"}` {
		t.Fatalf("request payload: %s", raw)
	}
	Emitter{}.RequestChanged(bg, tn, "r", "s", []string{"u"})
	Emitter{}.ReviewChanged(bg, tn, []string{"u"})
	Emitter{}.CalendarChanged(bg, tn, time.Now(), time.Now())
	if len(Types) != 3 {
		t.Fatal("types")
	}
}

func TestHubPublisher(t *testing.T) {
	HubPublisher{}.Publish(bg, tn, nil, CalendarChanged, nil)
	mem := stream.NewMemory()
	hub := stream.NewHub(mem, stream.Config{}, nil)
	defer hub.Close()
	HubPublisher{Hub: hub}.Publish(bg, "", nil, CalendarChanged, nil) // no tenant
	HubPublisher{Hub: hub}.Publish(bg, tn, nil, CalendarChanged, map[string]string{})
	HubPublisher{Hub: hub}.Publish(bg, tn, []string{"maria"}, RequestChanged, map[string]string{"request_id": "r"})
	if mem.Len(stream.Key(tn)) != 2 {
		t.Fatalf("stream entries: %d", mem.Len(stream.Key(tn)))
	}
}
