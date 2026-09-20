package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/tgs/server/internal/bus"
	"github.com/tgs/server/internal/domain"
	"github.com/tgs/server/internal/rules"
	"github.com/tgs/server/internal/sanction"
	"github.com/tgs/server/internal/secret"
	"github.com/tgs/server/internal/store"
	"github.com/tgs/server/internal/tgapi"
)

func newRelayRuntime(t *testing.T) *Runtime {
	t.Helper()
	r, _ := newTestRuntime(t, false, 0)
	groupID := int64(-1001234567890)
	row, err := r.db.CreateBot(context.Background(), store.CreateBotInput{
		Name: "relay", Username: "relay_bot", AdminGroupID: &groupID,
		Sealed:   secret.Sealed{Cipher: "c", IV: "i", Tag: "t"},
		Settings: store.DefaultBotSettings(0),
	})
	if err != nil {
		t.Fatal(err)
	}
	r.botID, r.botName, r.adminGroupID = row.ID, row.Name, groupID
	r.engine = rules.New(r.db.Read())
	r.sanction = sanction.New(r.db, r.log)
	r.bus = bus.New()
	r.loc = time.UTC
	r.queues = newKeyedQueue()
	r.floodWindow = make(map[int64][]time.Time)
	r.mutedNotified = make(map[int64]*int64)
	r.albums = make(map[string]*albumBuffer)
	return r
}

func privateMessage(id int64, text string) *tgapi.Message {
	return &tgapi.Message{
		MessageID: id, From: &tgapi.User{ID: 111, FirstName: "Visitor"},
		Chat: tgapi.Chat{ID: 111, Type: "private"}, Text: text,
	}
}

func TestRelayDispatchPersistsConversation(t *testing.T) {
	for _, kind := range []string{"text", "start", "album", "admin_reply"} {
		t.Run(kind, func(t *testing.T) {
			r := newRelayRuntime(t)
			ctx := context.Background()
			var mu sync.Mutex
			var calls []stubCall
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				var params map[string]any
				_ = json.NewDecoder(req.Body).Decode(&params)
				method := path.Base(req.URL.Path)
				mu.Lock()
				calls = append(calls, stubCall{method: method, params: params})
				mu.Unlock()
				var result any = true
				switch method {
				case "createForumTopic":
					result = map[string]any{"message_thread_id": 50, "name": "Visitor"}
				case "copyMessages":
					result = []map[string]any{{"message_id": 201}, {"message_id": 202}}
				case "sendMessage", "copyMessage":
					result = map[string]any{"message_id": 200}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
			}))
			defer srv.Close()
			r.api = tgapi.New("test", tgapi.Options{BaseURL: srv.URL, MinInterval: time.Millisecond})

			events := make(chan bus.Frame, 20)
			unsubscribe := r.bus.Subscribe(func(f bus.Frame) { events <- f })
			defer unsubscribe()
			m := privateMessage(10, "hello")
			wantMessages := 1
			wantDirection := domain.DirUserToAdmin
			key := m.From.ID
			switch kind {
			case "start":
				m.Text = "/start"
				wantMessages = 0
			case "admin_reply":
				contact, err := r.db.UpsertContact(ctx, r.botID, profileOf(m.From))
				if err != nil {
					t.Fatal(err)
				}
				topic, err := r.db.CreateTopic(ctx, r.botID, contact.ID, 50, "Visitor", 0x6FB9F0)
				if err != nil {
					t.Fatal(err)
				}
				key = -topic.ID
				m.Chat = tgapi.Chat{ID: r.adminGroupID, Type: "supergroup"}
				m.From = &tgapi.User{ID: 222, FirstName: "Admin"}
				m.IsTopicMessage, m.MessageThreadID = true, 50
				wantDirection = domain.DirAdminToUser
			case "album":
				m.MediaGroupID = "album-1"
				second := *m
				second.MessageID = 11
				r.albums[m.MediaGroupID] = &albumBuffer{
					fromUserID: m.From.ID, messages: []*tgapi.Message{m, &second},
				}
				r.queues.live.Add(1)
				wantMessages = 2
			}

			// Queue a probe behind the update so completion is independent of drain().
			if kind == "album" {
				r.flushAlbum(m.MediaGroupID)
			} else {
				r.dispatch(ctx, tgapi.Update{Message: m})
			}
			done := make(chan struct{})
			r.runSerial(ctx, key, func(context.Context) { close(done) })
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("relay did not complete")
			}

			sessions, err := r.db.ListSessions(ctx, store.SessionQuery{})
			if err != nil || len(sessions.Items) != 1 {
				t.Fatalf("expected one WebUI session, got %+v, err=%v", sessions, err)
			}
			messages, err := r.db.ListMessages(ctx, sessions.Items[0].ID, nil, 10)
			if err != nil || len(messages.Items) != wantMessages {
				t.Fatalf("expected %d persisted messages, got %+v, err=%v", wantMessages, messages, err)
			}
			for _, message := range messages.Items {
				if message.Direction != wantDirection {
					t.Errorf("direction = %s, want %s", message.Direction, wantDirection)
				}
			}
			created := false
			for len(events) > 0 {
				created = (<-events).Type == bus.EventSessionCreated || created
			}
			if kind != "admin_reply" && !created {
				t.Error("new conversation did not publish session.created for WebUI")
			}
			mu.Lock()
			defer mu.Unlock()
			var relayed bool
			for _, c := range calls {
				if kind == "start" {
					relayed = relayed || c.method == "sendMessage" && c.params["chat_id"] == float64(111)
				} else if kind == "album" {
					relayed = relayed || c.method == "copyMessages" && reflect.DeepEqual(c.params["message_ids"], []any{float64(10), float64(11)})
				} else if c.method == "copyMessage" {
					dest := r.adminGroupID
					if kind == "admin_reply" {
						dest = 111
					}
					relayed = c.params["chat_id"] == float64(dest)
				}
			}
			if !relayed {
				t.Errorf("expected Telegram delivery, calls=%+v", calls)
			}
		})
	}
}

func TestRunSerialOwnsContextAndDrains(t *testing.T) {
	r := newRelayRuntime(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	result := make(chan error, 1)
	r.runSerial(ctx, 111, func(taskCtx context.Context) {
		<-release
		if _, ok := taskCtx.Deadline(); !ok {
			result <- fmt.Errorf("relay task has no timeout")
			return
		}
		result <- taskCtx.Err()
	})
	cancel()
	drained := make(chan struct{})
	go func() {
		r.queues.drain()
		close(drained)
	}()
	select {
	case <-drained:
		t.Fatal("drain returned while a relay task was still active")
	case <-time.After(20 * time.Millisecond):
	}
	unblock()
	if err := <-result; err != nil {
		t.Fatalf("dispatch cancellation reached accepted relay task: %v", err)
	}
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("drain did not return after task completion")
	}
}

func TestAlbumTimerIsIncludedInDrain(t *testing.T) {
	r := newRelayRuntime(t)
	ctx := context.Background()
	// The existing stub is sufficient for checking persistence and completion.
	for id := int64(10); id < 12; id++ {
		m := privateMessage(id, "photo caption")
		m.MediaGroupID = "album-1"
		r.dispatch(ctx, tgapi.Update{Message: m})
	}
	r.queues.drain()
	r.albumMu.Lock()
	pending := len(r.albums)
	r.albumMu.Unlock()
	if pending != 0 {
		t.Fatalf("drain left %d buffered albums unprocessed", pending)
	}
	contact, err := r.db.GetContactByTgID(ctx, r.botID, 111)
	if err != nil || contact.ID == 0 {
		t.Fatalf("buffered album was canceled before processing: contact=%+v, err=%v", contact, err)
	}
}

func TestRuntimePollingLifecycle(t *testing.T) {
	for _, scenario := range []string{"webhook_failure", "conflict", "unauthorized", "success", "recovery"} {
		t.Run(scenario, func(t *testing.T) {
			r := newRelayRuntime(t)
			var mu sync.Mutex
			var methods []string
			var dropPending any
			polls := 0
			var statuses []string
			ready := make(chan struct{})
			var readyOnce sync.Once
			unsubscribe := r.bus.Subscribe(func(f bus.Frame) {
				if f.Type == bus.EventBotStatus {
					status := f.Payload.(domain.Bot).HealthStatus
					mu.Lock()
					statuses = append(statuses, status)
					mu.Unlock()
					if status == domain.HealthOnline {
						readyOnce.Do(func() { close(ready) })
					}
				}
			})
			defer unsubscribe()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				var params map[string]any
				_ = json.NewDecoder(req.Body).Decode(&params)
				method := path.Base(req.URL.Path)
				mu.Lock()
				methods = append(methods, method)
				if method == "deleteWebhook" {
					dropPending = params["drop_pending_updates"]
				}
				if method == "getUpdates" {
					polls++
				}
				poll := polls
				mu.Unlock()
				response := map[string]any{"ok": true, "result": true}
				fail := func(code int) {
					response = map[string]any{"ok": false, "error_code": code, "description": "test failure"}
				}
				switch method {
				case "getMe":
					response["result"] = map[string]any{"id": 123456, "is_bot": true, "username": "relay_bot"}
				case "deleteWebhook":
					if scenario == "webhook_failure" {
						fail(400)
					}
				case "getUpdates":
					switch {
					case scenario == "conflict":
						fail(409)
					case scenario == "unauthorized":
						fail(401)
					case scenario == "recovery" && poll == 1:
						fail(500)
					case poll == 1 || scenario == "recovery" && poll == 2:
						response["result"] = []any{}
					default:
						<-req.Context().Done()
						return
					}
				}
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer srv.Close()
			r.api = tgapi.New("test", tgapi.Options{BaseURL: srv.URL, MinInterval: time.Millisecond})
			defer r.Stop(context.Background())
			err := r.Start(context.Background())
			wantHealth := domain.HealthError
			if scenario == "webhook_failure" {
				if err == nil || r.IsRunning() {
					t.Fatalf("failed webhook removal must fail startup: err=%v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				finished := r.stopped
				if scenario == "success" || scenario == "recovery" {
					finished = ready
					wantHealth = domain.HealthOnline
				}
				select {
				case <-finished:
				case <-time.After(4 * time.Second):
					t.Fatal("polling did not reach expected state")
				}
				if r.IsRunning() != (wantHealth == domain.HealthOnline) {
					t.Fatalf("runtime running state disagrees with %s", wantHealth)
				}
			}
			row, err := r.db.GetBot(context.Background(), r.botID)
			if err != nil || row.HealthStatus != wantHealth {
				t.Fatalf("health=%s, want %s, err=%v", row.HealthStatus, wantHealth, err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(methods) < 2 || methods[0] != "getMe" || methods[1] != "deleteWebhook" || dropPending != false {
				t.Errorf("startup must remove webhook and preserve pending messages: calls=%v, drop=%v", methods, dropPending)
			}
			if scenario == "webhook_failure" && polls != 0 {
				t.Errorf("polled despite webhook removal failure")
			}
			if scenario == "recovery" && !reflect.DeepEqual(statuses, []string{domain.HealthStarting, domain.HealthError, domain.HealthOnline}) {
				t.Errorf("unexpected recovery status notifications: %v", statuses)
			}
		})
	}
}
