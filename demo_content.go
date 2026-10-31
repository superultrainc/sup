package main

// demoContents gives each demo PR its own description, history and diff, keyed by "repo#number".
var demoContents = map[string]demoContent{
	"backend-api#142": {
		body:     "## Summary\nAdds cookie-based sessions for the web dashboard and the desktop apps.\n\n## Why\nRight now every client sends the long-lived API key on each request. Sessions let us rotate credentials and revoke a single device.\n\n## Changes\n- `SessionStore` backed by Postgres with a 30 day sliding expiry\n- `/v1/auth/login`, `/v1/auth/logout`, `/v1/auth/refresh`\n- Middleware that accepts either a session cookie or the legacy key\n\n## Test plan\n- [x] Unit tests for expiry and refresh\n- [x] Manual login/logout from the dashboard\n- [ ] Load test on staging",
		commits:  []string{"Add sessions table and SessionStore", "Add login and logout handlers", "Accept session cookie in auth middleware", "Refresh sliding expiry on use", "Add tests for expired and revoked sessions"},
		comments: []demoComment{{"mike", "Do we want SameSite=Lax or Strict on the cookie? The desktop apps open the dashboard from a link."}, {"sarah", "Lax. Strict breaks the deep links. Added a note in the handler."}, {"alex", "Ran this against staging with the iOS build, login and refresh both work."}},
		review:   "Ship it. The expiry tests cover the cases I was worried about.",
		thread:   demoThread{path: "internal/auth/session.go", line: 20, author: "alex", body: "32 bytes from crypto/rand is plenty. Maybe worth a constant so the length isn't a magic number?", reply: "Sure, will pull it out into sessionIDBytes.", resolved: true},
		patch: `diff --git a/internal/auth/session.go b/internal/auth/session.go
new file mode 100644
index 0000000..183ebbd
--- /dev/null
+++ b/internal/auth/session.go
@@ -0,0 +1,29 @@
+package auth
+
+import (
+	"context"
+	"crypto/rand"
+	"encoding/hex"
+	"time"
+)
+
+const sessionTTL = 30 * 24 * time.Hour
+
+type Session struct {
+	ID        string
+	UserID    int64
+	ExpiresAt time.Time
+}
+
+func NewSession(ctx context.Context, db DB, userID int64) (*Session, error) {
+	buf := make([]byte, 32)
+	if _, err := rand.Read(buf); err != nil {
+		return nil, err
+	}
+	s := &Session{ID: hex.EncodeToString(buf), UserID: userID, ExpiresAt: time.Now().Add(sessionTTL)}
+	return s, db.InsertSession(ctx, s)
+}
+
+func (s *Session) Expired() bool {
+	return time.Now().After(s.ExpiresAt)
+}
diff --git a/internal/http/middleware.go b/internal/http/middleware.go
index a0b6db7..80af92a 100644
--- a/internal/http/middleware.go
+++ b/internal/http/middleware.go
@@ -18,6 +18,12 @@ func Auth(next http.Handler) http.Handler {
 		key := r.Header.Get("X-API-Key")
-		if key == "" {
-			http.Error(w, "unauthorized", http.StatusUnauthorized)
-			return
-		}
+		if c, err := r.Cookie("sid"); err == nil {
+			if s, err := sessions.Lookup(r.Context(), c.Value); err == nil && !s.Expired() {
+				next.ServeHTTP(w, r.WithContext(withUser(r.Context(), s.UserID)))
+				return
+			}
+		}
+		if key == "" {
+			http.Error(w, "unauthorized", http.StatusUnauthorized)
+			return
+		}
 		next.ServeHTTP(w, r)
`,
	},
	"job-runner#287": {
		body:     "Fixes the slow leak we saw in the worker pool on the nightly soak test (SU-2231).\n\n![Pool memory over 12h: before (red) and after (green)](demo:memory)\n\nEach finished job left its result channel referenced from the pool's `inflight` map, so memory grew by about 4 MB per 10k jobs. Entries are now removed when the job completes, and the old `Drain` helper that papered over it is gone.\n\nBefore: RSS 1.9 GB after 12h.\nAfter: flat at 310 MB.",
		commits:  []string{"Delete inflight entry when a job finishes", "Remove the Drain workaround"},
		comments: []demoComment{{"sarah", "Nice catch. Was this in the heap profile from last week?"}, {"alex", "Yes, 70% of live heap was the result structs held by that map."}},
		review:   "Deleting the entry is right, but it happens before j.done is read on the error path, so a cancelled job still leaks. Can you also delete in the ctx.Done() branch and add a test for it?",
		thread:   demoThread{path: "internal/pool/pool.go", line: 43, author: "sarah", body: "What about jobs that panic? This line is skipped if Execute panics.", reply: "", resolved: false},
		patch: `diff --git a/internal/pool/pool.go b/internal/pool/pool.go
index 00336a9..54b2cbf 100644
--- a/internal/pool/pool.go
+++ b/internal/pool/pool.go
@@ -41,15 +41,6 @@ func (p *Pool) run(j *Job) {
 	res := j.Execute(ctx)
-	p.mu.Lock()
-	p.inflight[j.ID] = res
-	p.mu.Unlock()
+	p.mu.Lock()
+	delete(p.inflight, j.ID)
+	p.mu.Unlock()
 	j.done <- res
 }
-
-// Drain clears results nobody read.
-func (p *Pool) Drain() {
-	p.mu.Lock()
-	defer p.mu.Unlock()
-	for id := range p.inflight {
-		delete(p.inflight, id)
-	}
-}
diff --git a/internal/pool/pool_test.go b/internal/pool/pool_test.go
index 9536fe2..fd0f1a8 100644
--- a/internal/pool/pool_test.go
+++ b/internal/pool/pool_test.go
@@ -60,3 +60,5 @@ func TestPoolRuns(t *testing.T) {
 	p.Wait()
-	p.Drain()
+	if n := len(p.inflight); n != 0 {
+		t.Fatalf("inflight = %d, want 0", n)
+	}
 }
`,
	},
	"web-app#91": {
		body:     "Moves the dashboard cards onto the new metrics API.\n\n| Card | Source before | Source now |\n|------|---------------|------------|\n| Words | /api/stats | /api/metrics/words |\n| Time saved | computed client side | /api/metrics/time-saved |\n| Streak | /api/stats | /api/metrics/streak |\n\nStill to do: loading skeletons.",
		commits:  []string{"Add useMetric hook", "Port Words and Streak cards", "Port Time saved card to the API value"},
		comments: []demoComment{{"taylor", "Can we keep the old hook until the mobile web view moves over?"}},
		thread:   demoThread{path: "app/dashboard/useMetric.ts", line: 6, author: "taylor", body: "Should range be a union type instead of a string? Easy to typo.", reply: "Yes, will do in the follow-up with the range picker.", resolved: false},
		patch: `diff --git a/app/dashboard/useMetric.ts b/app/dashboard/useMetric.ts
new file mode 100644
index 0000000..9060674
--- /dev/null
+++ b/app/dashboard/useMetric.ts
@@ -0,0 +1,10 @@
+import useSWR from "swr";
+
+export type Metric = "words" | "time-saved" | "streak";
+
+export function useMetric(metric: Metric, range = "30d") {
+  const { data, error, isLoading } = useSWR<{ value: number }>(
+    "/api/metrics/" + metric + "?range=" + range
+  );
+  return { value: data?.value ?? 0, error, isLoading };
+}
diff --git a/app/dashboard/WordsCard.tsx b/app/dashboard/WordsCard.tsx
index d479b4b..19f80bd 100644
--- a/app/dashboard/WordsCard.tsx
+++ b/app/dashboard/WordsCard.tsx
@@ -1,7 +1,7 @@
-import { useStats } from "./useStats";
+import { useMetric } from "./useMetric";
 import { Card } from "@/components/Card";
 
 export function WordsCard() {
-  const stats = useStats();
-  return <Card title="Words" value={stats.words.toLocaleString()} />;
+  const { value, isLoading } = useMetric("words");
+  return <Card title="Words" value={value.toLocaleString()} loading={isLoading} />;
 }
`,
	},
	"backend-api#445": {
		body:    "WIP: token bucket rate limiter as chi middleware.\n\n- [x] In-memory bucket per API key\n- [ ] Redis backend for multiple instances\n- [ ] Headers: `X-RateLimit-Remaining`, `Retry-After`\n- [ ] Per-route overrides",
		commits: []string{"Add token bucket implementation", "Wire limiter into router (disabled by default)"},
		patch: `diff --git a/internal/ratelimit/bucket.go b/internal/ratelimit/bucket.go
new file mode 100644
index 0000000..8325a8c
--- /dev/null
+++ b/internal/ratelimit/bucket.go
@@ -0,0 +1,34 @@
+package ratelimit
+
+import (
+	"sync"
+	"time"
+)
+
+type Bucket struct {
+	mu       sync.Mutex
+	tokens   float64
+	capacity float64
+	rate     float64
+	last     time.Time
+}
+
+func NewBucket(capacity, perSecond float64) *Bucket {
+	return &Bucket{tokens: capacity, capacity: capacity, rate: perSecond, last: time.Now()}
+}
+
+func (b *Bucket) Allow() bool {
+	b.mu.Lock()
+	defer b.mu.Unlock()
+	now := time.Now()
+	b.tokens += now.Sub(b.last).Seconds() * b.rate
+	if b.tokens > b.capacity {
+		b.tokens = b.capacity
+	}
+	b.last = now
+	if b.tokens < 1 {
+		return false
+	}
+	b.tokens--
+	return true
+}
diff --git a/internal/http/router.go b/internal/http/router.go
index f0aa510..982e567 100644
--- a/internal/http/router.go
+++ b/internal/http/router.go
@@ -27,3 +27,6 @@ func NewRouter(cfg Config) http.Handler {
 	r := chi.NewRouter()
 	r.Use(Auth)
+	if cfg.RateLimit.Enabled {
+		r.Use(ratelimit.Middleware(cfg.RateLimit))
+	}
 	r.Mount("/v1", v1Routes())
`,
	},
	"data-service#156": {
		body:     "Switches the data service from one connection per request to a `pgxpool` pool.\n\nPool size defaults to 20 and can be set with `PG_MAX_CONNS`. Health check pings run every 30s so dead connections get replaced.\n\nFixes #151",
		commits:  []string{"Use pgxpool instead of pgx.Connect", "Make pool size configurable", "Add connection health checks"},
		comments: []demoComment{{"priya", "What is the Postgres max_connections on prod? With 6 replicas x 20 we are at 120."}, {"chris", "Prod is 200 but the job runner uses 40. I'll drop the default to 15 to leave room."}},
		review:   "Does the pool get closed on shutdown? I don't see a Close call in main.go.",
		thread:   demoThread{path: "internal/store/db.go", line: 26, author: "priya", body: "Nit: pull the 20 into a named constant so it shows up in grep.", reply: "Done.", resolved: true},
		patch: `diff --git a/internal/store/db.go b/internal/store/db.go
index 956d1f7..0a6cbce 100644
--- a/internal/store/db.go
+++ b/internal/store/db.go
@@ -12,9 +12,21 @@
 import (
 	"context"
+	"os"
+	"strconv"
+	"time"
 
-	"github.com/jackc/pgx/v5"
+	"github.com/jackc/pgx/v5/pgxpool"
 )
 
-func Open(ctx context.Context, url string) (*pgx.Conn, error) {
-	return pgx.Connect(ctx, url)
+func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
+	cfg, err := pgxpool.ParseConfig(url)
+	if err != nil {
+		return nil, err
+	}
+	cfg.MaxConns = 20
+	if v, err := strconv.Atoi(os.Getenv("PG_MAX_CONNS")); err == nil && v > 0 {
+		cfg.MaxConns = int32(v)
+	}
+	cfg.HealthCheckPeriod = 30 * time.Second
+	return pgxpool.NewWithConfig(ctx, cfg)
 }
`,
	},
	"backend-api#312": {
		body:     "## Summary\nSplits the notification service into a `Notifier` interface with email and push implementations. The old `SendAll` function had grown to 400 lines of nested switches.\n\n## Notes\n- No behavior change intended\n- Templates moved to `internal/notify/templates`\n- Deleted the unused SMS path (it was never enabled)\n\nReviewers: start with `notifier.go`, the rest is mechanical moves.",
		commits:  []string{"Extract Notifier interface", "Move email sending into EmailNotifier", "Move push sending into PushNotifier", "Delete SMS code", "Update callers to use the registry"},
		comments: []demoComment{{"alex", "Diff looks scary but the moves are clean. Reviewed commit by commit."}},
		review:   "Approved. The registry is much easier to follow than SendAll.",
		thread:   demoThread{path: "internal/notify/notifier.go", line: 29, author: "jordan", body: "Should one failing notifier stop the rest? Right now we keep going and return the first error, which seems right, just checking it's intended.", reply: "Intended. A push failure shouldn't block the email.", resolved: true},
		patch: `diff --git a/internal/notify/notifier.go b/internal/notify/notifier.go
new file mode 100644
index 0000000..6423feb
--- /dev/null
+++ b/internal/notify/notifier.go
@@ -0,0 +1,33 @@
+package notify
+
+import "context"
+
+type Message struct {
+	UserID  int64
+	Kind    string
+	Subject string
+	Body    string
+}
+
+type Notifier interface {
+	Name() string
+	Send(ctx context.Context, m Message) error
+}
+
+type Registry struct {
+	notifiers []Notifier
+}
+
+func (r *Registry) Register(n Notifier) {
+	r.notifiers = append(r.notifiers, n)
+}
+
+func (r *Registry) Send(ctx context.Context, m Message) error {
+	var firstErr error
+	for _, n := range r.notifiers {
+		if err := n.Send(ctx, m); err != nil && firstErr == nil {
+			firstErr = err
+		}
+	}
+	return firstErr
+}
diff --git a/internal/notify/sms.go b/internal/notify/sms.go
deleted file mode 100644
index d4294c4..0000000
--- a/internal/notify/sms.go
+++ /dev/null
@@ -1,9 +0,0 @@
-package notify
-
-import "context"
-
-type smsSender struct{ client *twilioClient }
-
-func (s *smsSender) Send(ctx context.Context, to, body string) error {
-	return s.client.Messages.Create(ctx, to, body)
-}
`,
	},
	"web-app#78": {
		body:     "Adds a dark theme to the marketing site and docs links.\n\n- Uses `prefers-color-scheme` by default\n- Toggle in the footer stores the choice in `localStorage`\n- All colors now come from CSS variables in `theme.css`\n\nScreenshots in the thread.",
		commits:  []string{"Move colors to CSS variables", "Add dark palette", "Add theme toggle to footer", "Fix contrast on pricing cards"},
		comments: []demoComment{{"priya", "Looks great on the pricing page. The gray on the FAQ is a bit low contrast in dark."}, {"sam", "Bumped --muted a step, thanks."}},
		thread:   demoThread{path: "components/ThemeToggle.tsx", line: 8, author: "priya", body: "This runs after first paint so there is a flash of the wrong theme. Inline script in the head?", reply: "", resolved: false},
		patch: `diff --git a/styles/theme.css b/styles/theme.css
new file mode 100644
index 0000000..242da4f
--- /dev/null
+++ b/styles/theme.css
@@ -0,0 +1,22 @@
+:root {
+  --bg: #ffffff;
+  --fg: #111418;
+  --muted: #5b6573;
+  --card: #f5f6f8;
+}
+
+@media (prefers-color-scheme: dark) {
+  :root:not([data-theme="light"]) {
+    --bg: #0e1013;
+    --fg: #eef0f3;
+    --muted: #9aa3b0;
+    --card: #181b20;
+  }
+}
+
+:root[data-theme="dark"] {
+  --bg: #0e1013;
+  --fg: #eef0f3;
+  --muted: #9aa3b0;
+  --card: #181b20;
+}
diff --git a/components/ThemeToggle.tsx b/components/ThemeToggle.tsx
new file mode 100644
index 0000000..8b756b2
--- /dev/null
+++ b/components/ThemeToggle.tsx
@@ -0,0 +1,22 @@
+"use client";
+import { useEffect, useState } from "react";
+
+export function ThemeToggle() {
+  const [theme, setTheme] = useState<"light" | "dark">("light");
+
+  useEffect(() => {
+    const saved = localStorage.getItem("theme");
+    if (saved === "dark" || saved === "light") setTheme(saved);
+  }, []);
+
+  useEffect(() => {
+    document.documentElement.dataset.theme = theme;
+    localStorage.setItem("theme", theme);
+  }, [theme]);
+
+  return (
+    <button onClick={() => setTheme(theme === "dark" ? "light" : "dark")}>
+      {theme === "dark" ? "Light" : "Dark"} mode
+    </button>
+  );
+}
`,
	},
	"cli-tools#203": {
		body:    "Bumps the toolchain to Go 1.22 in `go.mod`, CI and the Dockerfile. Nothing else changes.",
		commits: []string{"Bump go directive to 1.22", "Update CI and Dockerfile to 1.22"},
		thread:  demoThread{path: "Dockerfile", line: 1, author: "sam", body: "Might as well pin the alpine version too so builds are reproducible.", reply: "", resolved: false},
		patch: `diff --git a/go.mod b/go.mod
index 37aff10..2a57d3d 100644
--- a/go.mod
+++ b/go.mod
@@ -1,5 +1,5 @@
 module github.com/acme-corp/cli-tools
 
-go 1.21
+go 1.22
 
 require (
diff --git a/.github/workflows/ci.yml b/.github/workflows/ci.yml
index e9f950f..17198d3 100644
--- a/.github/workflows/ci.yml
+++ b/.github/workflows/ci.yml
@@ -14,4 +14,4 @@ jobs:
       - uses: actions/setup-go@v5
         with:
-          go-version: "1.21"
+          go-version: "1.22"
       - run: make test
diff --git a/Dockerfile b/Dockerfile
index 3254677..a7917c6 100644
--- a/Dockerfile
+++ b/Dockerfile
@@ -1,3 +1,3 @@
-FROM golang:1.21-alpine AS build
+FROM golang:1.22-alpine AS build
 WORKDIR /src
 COPY . .
`,
	},
	"macos-app#1048": {
		body:     "Adds **Today** and **This Month** to the stats range picker, so recent usage is one click away.\n\n![Stats with the new range picker](demo:stats)\n\n### Changes\n- New `StatsRange` enum replaces the old string keys\n- Picker shows Today, 7 days, 30 days, This Month, All time\n- Month boundary uses the user's calendar, not UTC\n\nFixes SU-1982.",
		commits:  []string{"Add StatsRange enum", "Wire Today and This Month into the picker", "Use the user's calendar for month boundaries", "Fix off-by-one at month start"},
		comments: []demoComment{{"sarah", "Tried it around midnight with the clock changed, Today rolls over correctly."}, {"mike", "Will the saved range from older versions still load?"}},
		thread:   demoThread{path: "App/Stats/StatsRange.swift", line: 23, author: "mike", body: "What happens for weeks that start on Monday vs Sunday? Not relevant for month, just checking we don't need it.", reply: "Right, month only. Week is a rolling 7 days.", resolved: true},
		patch: `diff --git a/App/Stats/StatsRange.swift b/App/Stats/StatsRange.swift
new file mode 100644
index 0000000..2f78751
--- /dev/null
+++ b/App/Stats/StatsRange.swift
@@ -0,0 +1,27 @@
+import Foundation
+
+enum StatsRange: String, CaseIterable, Identifiable {
+    case today, week, month30, thisMonth, all
+
+    var id: String { rawValue }
+
+    var title: String {
+        switch self {
+        case .today: "Today"
+        case .week: "7 days"
+        case .month30: "30 days"
+        case .thisMonth: "This Month"
+        case .all: "All time"
+        }
+    }
+
+    func startDate(now: Date = .now, calendar: Calendar = .current) -> Date? {
+        switch self {
+        case .today: calendar.startOfDay(for: now)
+        case .week: calendar.date(byAdding: .day, value: -7, to: now)
+        case .month30: calendar.date(byAdding: .day, value: -30, to: now)
+        case .thisMonth: calendar.dateInterval(of: .month, for: now)?.start
+        case .all: nil
+        }
+    }
+}
diff --git a/App/Stats/StatsRangePicker.swift b/App/Stats/StatsRangePicker.swift
index 1efc8ba..0c94239 100644
--- a/App/Stats/StatsRangePicker.swift
+++ b/App/Stats/StatsRangePicker.swift
@@ -8,8 +8,8 @@ struct StatsRangePicker: View {
-    @Binding var range: String
+    @Binding var range: StatsRange
 
     var body: some View {
         Picker("Range", selection: $range) {
-            Text("7 days").tag("7d")
-            Text("30 days").tag("30d")
-            Text("All time").tag("all")
+            ForEach(StatsRange.allCases) { r in
+                Text(r.title).tag(r)
+            }
         }
`,
	},
	"macos-app#1046": {
		body:    "Recording no longer pulls focus from the frontmost app, so typing continues uninterrupted.",
		commits: []string{"Show recording panel without activating the app"},
		review:  "Ship it.",
		thread:  demoThread{path: "App/Recording/RecordingPanel.swift", line: 32, author: "jordan", body: "Confirmed this keeps the cursor in Slack while recording.", reply: "", resolved: true},
		patch: `diff --git a/App/Recording/RecordingPanel.swift b/App/Recording/RecordingPanel.swift
index e06e2e0..8fd7a10 100644
--- a/App/Recording/RecordingPanel.swift
+++ b/App/Recording/RecordingPanel.swift
@@ -31,3 +31,3 @@ func show() {
     panel.level = .statusBar
-    panel.makeKeyAndOrderFront(nil)
+    panel.orderFrontRegardless()
 }
`,
	},
	"macos-app#1045": {
		body:     "## Why\nLicense data in `UserDefaults` can be read by any process running as the user and is easy to copy between machines. The keychain gives us encryption at rest and access control.\n\n## Migration\nOn first launch of this version we read the old keys, write them to the keychain, then delete the old keys. If the keychain write fails we keep the old values and try again next launch.\n\n## Tests\n- `LicenseStoreTests` covers fresh install, migration, and a failed write\n- Manually tested upgrading from 2.9.4",
		commits:  []string{"Add KeychainStore wrapper", "Move LicenseStore to the keychain", "Migrate existing UserDefaults values", "Delete old license cache types", "Add migration tests"},
		comments: []demoComment{{"alex", "Does the migration run before the first license check? Otherwise we flash the paywall."}, {"sarah", "Yes, it runs in applicationWillFinishLaunching."}},
		thread:   demoThread{path: "App/License/KeychainStore.swift", line: 26, author: "alex", body: "Delete then add loses the item if the add fails. SecItemUpdate first?", reply: "", resolved: false},
		patch: `diff --git a/App/License/KeychainStore.swift b/App/License/KeychainStore.swift
new file mode 100644
index 0000000..56ad033
--- /dev/null
+++ b/App/License/KeychainStore.swift
@@ -0,0 +1,31 @@
+import Foundation
+import Security
+
+struct KeychainStore {
+    let service: String
+
+    func read(_ key: String) -> Data? {
+        let query: [CFString: Any] = [
+            kSecClass: kSecClassGenericPassword,
+            kSecAttrService: service,
+            kSecAttrAccount: key,
+            kSecReturnData: true,
+        ]
+        var out: CFTypeRef?
+        guard SecItemCopyMatching(query as CFDictionary, &out) == errSecSuccess else { return nil }
+        return out as? Data
+    }
+
+    @discardableResult
+    func write(_ key: String, _ data: Data) -> Bool {
+        let query: [CFString: Any] = [
+            kSecClass: kSecClassGenericPassword,
+            kSecAttrService: service,
+            kSecAttrAccount: key,
+        ]
+        SecItemDelete(query as CFDictionary)
+        var add = query
+        add[kSecValueData] = data
+        return SecItemAdd(add as CFDictionary, nil) == errSecSuccess
+    }
+}
diff --git a/App/License/LicenseStore.swift b/App/License/LicenseStore.swift
index daf7a41..b75d548 100644
--- a/App/License/LicenseStore.swift
+++ b/App/License/LicenseStore.swift
@@ -20,6 +20,6 @@ final class LicenseStore {
-    private let defaults = UserDefaults.standard
+    private let keychain = KeychainStore(service: "com.acme.license")
 
     var licenseKey: String? {
-        get { defaults.string(forKey: "licenseKey") }
-        set { defaults.set(newValue, forKey: "licenseKey") }
+        get { keychain.read("licenseKey").flatMap { String(data: $0, encoding: .utf8) } }
+        set { keychain.write("licenseKey", Data((newValue ?? "").utf8)) }
     }
`,
	},
	"macos-app#1044": {
		body:    "Updates now download in the background. If one is ready when you quit, it installs on the way out and the next launch is the new version.\n\n- Respects the existing \"Check for updates automatically\" setting\n- Skips install if a recording is in progress\n- Adds a line to the settings pane explaining it",
		commits: []string{"Download updates in the background", "Install pending update from applicationShouldTerminate", "Add settings caption"},
		review:  "Nice, works as described. Tested quitting mid-download too.",
		thread:  demoThread{path: "App/Updates/UpdateController.swift", line: 45, author: "sam", body: "Good, we don't want to kill a recording for an update.", reply: "", resolved: true},
		patch: `diff --git a/App/Updates/UpdateController.swift b/App/Updates/UpdateController.swift
index 7f60b1c..6c01312 100644
--- a/App/Updates/UpdateController.swift
+++ b/App/Updates/UpdateController.swift
@@ -44,3 +44,9 @@ extension UpdateController: SPUUpdaterDelegate {
+    func updater(_ updater: SPUUpdater, willInstallUpdateOnQuit item: SUAppcastItem, immediateInstallationBlock block: @escaping () -> Void) -> Bool {
+        guard !Recorder.shared.isRecording else { return false }
+        pendingInstall = block
+        return true
+    }
+
     func updaterDidNotFindUpdate(_ updater: SPUUpdater) {
         state = .upToDate
     }
diff --git a/App/AppDelegate.swift b/App/AppDelegate.swift
index 2e9f022..9b7e776 100644
--- a/App/AppDelegate.swift
+++ b/App/AppDelegate.swift
@@ -90,3 +90,7 @@
 func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
+    if let install = updates.pendingInstall {
+        install()
+        return .terminateLater
+    }
     return .terminateNow
 }
`,
	},
	"macos-app#1042": {
		body:     "Points the appcast at `updates.acme.example` (CDN) instead of the app server.\n\nOld URL keeps working for installed versions until 3.0 ships.",
		commits:  []string{"Switch SUFeedURL to the CDN"},
		comments: []demoComment{{"sarah", "CDN cache is 5 minutes, fine for appcast."}},
		review:   "LGTM",
		thread:   demoThread{path: "scripts/publish_appcast.sh", line: 10, author: "sarah", body: "Is $CDN_ID set in the release workflow?", reply: "Yes, added to the repo variables yesterday.", resolved: true},
		patch: `diff --git a/App/Info.plist b/App/Info.plist
index 7e0b943..67fb722 100644
--- a/App/Info.plist
+++ b/App/Info.plist
@@ -31,4 +31,4 @@
 	<key>SUFeedURL</key>
-	<string>https://app.acme.example/updates/appcast.xml</string>
+	<string>https://updates.acme.example/appcast.xml</string>
 	<key>SUEnableAutomaticChecks</key>
 	<true/>
diff --git a/scripts/publish_appcast.sh b/scripts/publish_appcast.sh
index f8e3ecb..8488b4b 100644
--- a/scripts/publish_appcast.sh
+++ b/scripts/publish_appcast.sh
@@ -8,2 +8,3 @@
 aws s3 cp build/appcast.xml s3://acme-app-releases/appcast.xml
-aws s3 cp build/appcast.xml s3://acme-app-server/updates/appcast.xml
+aws s3 cp build/appcast.xml s3://acme-app-cdn/appcast.xml
+aws cloudfront create-invalidation --distribution-id "$CDN_ID" --paths "/appcast.xml"
`,
	},
	"macos-app#1041": {
		body:     "Release builds were shipping debug symbols and every localization we ever had.\n\n| | Before | After |\n|---|---|---|\n| App | 212 MB | 141 MB |\n| DMG | 188 MB | 124 MB |\n\nStrips symbols into a separate dSYM upload and drops localizations we don't translate.",
		commits:  []string{"Strip debug symbols from release builds", "Remove unused localizations from the bundle"},
		comments: []demoComment{{"jordan", "141 MB, nice. Does Sentry still symbolicate?"}, {"alex", "Yes, dSYMs upload before the strip step."}},
		review:   "Will the crash reporter still get symbols? The dSYM upload happens in a different script, so I'd like to see it run before strip.",
		thread:   demoThread{path: "scripts/build_release.sh", line: 23, author: "jordan", body: "strip after codesign would invalidate the signature, but here it is before so fine. Worth a comment saying the order matters.", reply: "", resolved: false},
		patch: `diff --git a/scripts/build_release.sh b/scripts/build_release.sh
index f0f2398..ca160a7 100644
--- a/scripts/build_release.sh
+++ b/scripts/build_release.sh
@@ -22,2 +22,9 @@
 xcodebuild -scheme App -configuration Release archive
+strip -x "$APP/Contents/MacOS/App"
+for lproj in "$APP"/Contents/Resources/*.lproj; do
+  case "$(basename "$lproj")" in
+    en.lproj|de.lproj|es.lproj|fr.lproj|ja.lproj) ;;
+    *) rm -rf "$lproj" ;;
+  esac
+done
 codesign --deep --options runtime "$APP"
`,
	},
	"macos-app#1038": {
		body:     "## Summary\nAfter you stop a meeting recording there is now a **Resume** button. Resuming keeps the same transcript and continues the timeline where it ended.\n\n![Resumed meeting, same transcript](demo:meeting)\n\n## Details\n- `MeetingSession` gains a `.paused` state\n- Audio from the new segment is appended to the same file\n- Speaker labels carry over, so \"Speaker 2\" stays \"Speaker 2\"\n\n## Known gaps\n- Resuming after the app restarts is not supported yet",
		commits:  []string{"Add paused state to MeetingSession", "Append audio to the existing file on resume", "Carry speaker labels across segments", "Add Resume button to meeting detail", "Handle resume after a crash"},
		comments: []demoComment{{"sarah", "Should the timestamps keep counting through the gap or skip it?"}, {"taylor", "Skip it. Segment offset is the transcript duration, not wall clock."}},
		review:   "Resuming only works while the app stays open, but the Resume button shows for meetings from last week too and does nothing. Please hide it for old meetings or support reopening the audio file.",
		thread:   demoThread{path: "App/Meetings/MeetingSession.swift", line: 23, author: "sarah", body: "If reopen throws we have already set segmentOffset. Move it after?", reply: "", resolved: false},
		patch: `diff --git a/App/Meetings/MeetingSession.swift b/App/Meetings/MeetingSession.swift
index 91d1026..f23fa15 100644
--- a/App/Meetings/MeetingSession.swift
+++ b/App/Meetings/MeetingSession.swift
@@ -15,8 +15,16 @@ final class MeetingSession: ObservableObject {
-    enum State { case idle, recording, finished }
+    enum State { case idle, recording, paused, finished }
 
     @Published private(set) var state: State = .idle
+    private var segmentOffset: TimeInterval = 0
 
+    func resume() throws {
+        guard state == .paused || state == .finished else { return }
+        segmentOffset = transcript.duration
+        try writer.reopen(appending: true)
+        state = .recording
+    }
+
     func stop() {
         writer.close()
-        state = .finished
+        state = .paused
     }
diff --git a/App/Meetings/MeetingDetailView.swift b/App/Meetings/MeetingDetailView.swift
index 36d6b84..4a51559 100644
--- a/App/Meetings/MeetingDetailView.swift
+++ b/App/Meetings/MeetingDetailView.swift
@@ -52,3 +52,6 @@ var actions: some View {
     HStack {
         Button("Export") { export() }
+        if session.state == .paused {
+            Button("Resume") { try? session.resume() }
+        }
     }
`,
	},
	"macos-app#1032": {
		body:     "Local models can take 1-3 GB of RAM. After 10 minutes with no dictation we now unload them, and load again on the next hotkey press.\n\nFirst dictation after an unload takes about 0.8s longer (measured on M2, base model). There's a toggle in Settings > Advanced to turn it off.\n\nFixes SU-2045",
		commits:  []string{"Track last dictation time", "Add idle timer that unloads the model", "Reload lazily on next use", "Add Settings toggle", "Cancel unload when a recording starts"},
		comments: []demoComment{{"alex", "Is 10 minutes configurable?"}, {"chris", "Constant for now, the toggle is on/off only."}, {"alex", "Memory over an hour with the toggle on:\n\n![Memory with idle unload](demo:memory)"}},
		thread:   demoThread{path: "App/Models/IdleUnloader.swift", line: 16, author: "alex", body: "Timers on the main run loop pause during menu tracking. Dispatch source instead?", reply: "", resolved: false},
		patch: `diff --git a/App/Models/IdleUnloader.swift b/App/Models/IdleUnloader.swift
new file mode 100644
index 0000000..1244e73
--- /dev/null
+++ b/App/Models/IdleUnloader.swift
@@ -0,0 +1,25 @@
+import Foundation
+
+@MainActor
+final class IdleUnloader {
+    private var timer: Timer?
+    private let interval: TimeInterval
+    private let unload: () -> Void
+
+    init(interval: TimeInterval = 600, unload: @escaping () -> Void) {
+        self.interval = interval
+        self.unload = unload
+    }
+
+    func touch() {
+        timer?.invalidate()
+        timer = Timer.scheduledTimer(withTimeInterval: interval, repeats: false) { [unload] _ in
+            unload()
+        }
+    }
+
+    func cancel() {
+        timer?.invalidate()
+        timer = nil
+    }
+}
diff --git a/App/Models/ModelManager.swift b/App/Models/ModelManager.swift
index 6e9a525..e19d2d7 100644
--- a/App/Models/ModelManager.swift
+++ b/App/Models/ModelManager.swift
@@ -33,3 +33,4 @@ func transcribe(_ audio: AudioBuffer) async throws -> String {
+    unloader.touch()
+    if model == nil { model = try await loadModel(selected) }
-    let model = try await loadModel(selected)
-    return try await model.transcribe(audio)
+    return try await model!.transcribe(audio)
 }
`,
	},
	"macos-app#1029": {
		body:    "Draft, mostly to try the animation.\n\n![Menu bar while recording](demo:menubar)\n\nThe menu bar icon fills from the bottom with the live input level while recording. Needs a smoothing pass, it flickers on quiet input.",
		commits: []string{"Publish input level from the recorder", "Draw level fill in the status item image"},
		thread:  demoThread{path: "App/MenuBar/StatusItemController.swift", line: 64, author: "priya", body: "Redrawing an image per audio buffer will be hot. Draw into a CALayer mask instead?", reply: "Yes, switching to a layer before taking this out of draft.", resolved: false},
		patch: `diff --git a/App/MenuBar/StatusItemController.swift b/App/MenuBar/StatusItemController.swift
index aeb59b0..d914fa1 100644
--- a/App/MenuBar/StatusItemController.swift
+++ b/App/MenuBar/StatusItemController.swift
@@ -60,2 +60,8 @@ private func render(level: Float) {
-    item.button?.image = NSImage(named: "mic")
+    let size = NSSize(width: 18, height: 18)
+    item.button?.image = NSImage(size: size, flipped: false) { rect in
+        NSImage(named: "mic")?.draw(in: rect)
+        NSColor.systemRed.setFill()
+        NSRect(x: 0, y: 0, width: rect.width, height: rect.height * CGFloat(level)).fill(using: .sourceAtop)
+        return true
+    }
 }
`,
	},
	"macos-app#1027": {
		body:     "### Problem\nUnplugging a USB mic during a recording crashed with `AVAudioEngine` configuration change assertion (SU-2190, 41 crashes last week).\n\n### Fix\nObserve `AVAudioEngineConfigurationChange`, stop the engine, save what we have, and show a notice.\n\n### Tested\n- USB mic unplugged while recording: audio kept, notice shown\n- AirPods disconnect: same",
		commits:  []string{"Observe AVAudioEngineConfigurationChange", "Stop and save the recording on device loss", "Show a notice when input disappears"},
		comments: []demoComment{{"mike", "Reproduced the crash on main with a USB-C mic, no crash on this branch."}},
		review:   "Works with my USB mic and AirPods. Ship it.",
		thread:   demoThread{path: "App/Recording/Recorder.swift", line: 77, author: "mike", body: "Is a zero sample rate the right signal, or can the format change to a different valid rate when switching devices?", reply: "Different valid rate is a normal switch, we keep recording then. Zero means no input.", resolved: true},
		patch: `diff --git a/App/Recording/Recorder.swift b/App/Recording/Recorder.swift
index d6b4d8e..bb26a6f 100644
--- a/App/Recording/Recorder.swift
+++ b/App/Recording/Recorder.swift
@@ -70,4 +70,13 @@ func start() throws {
     try engine.start()
+    NotificationCenter.default.addObserver(forName: .AVAudioEngineConfigurationChange, object: engine, queue: .main) { [weak self] _ in
+        self?.inputDidChange()
+    }
 }
 
+private func inputDidChange() {
+    guard isRecording, engine.inputNode.inputFormat(forBus: 0).sampleRate == 0 else { return }
+    stopAndSave()
+    Notice.show("Microphone disconnected. Your recording was saved.")
+}
+
 func stop() {
`,
	},
	"ios-app#261": {
		body:     "Swipe left on the keyboard to delete the previous word, like the system keyboard.\n\n![Swipe left to delete a word](demo:keyboard)\n\n- Threshold: 40pt horizontal, less than 20pt vertical drift\n- Repeats while held? Not yet, one word per swipe\n- Haptic tick on delete (if haptics are on)",
		commits:  []string{"Add swipe recognizer to the keyboard view", "Delete previous word via textDocumentProxy", "Ignore swipes that start on a key popup"},
		comments: []demoComment{{"sam", "Feels great. Does it work in password fields?"}},
		thread:   demoThread{path: "Keyboard/KeyboardViewController.swift", line: 53, author: "sam", body: "This is nil in secure fields, so swipe does nothing there. Probably fine, but should we fall back to deleting one character?", reply: "", resolved: false},
		patch: `diff --git a/Keyboard/KeyboardViewController.swift b/Keyboard/KeyboardViewController.swift
index a36ae56..821a648 100644
--- a/Keyboard/KeyboardViewController.swift
+++ b/Keyboard/KeyboardViewController.swift
@@ -44,4 +44,14 @@ override func viewDidLoad() {
     super.viewDidLoad()
     setupKeys()
+    let swipe = UISwipeGestureRecognizer(target: self, action: #selector(deleteWord))
+    swipe.direction = .left
+    view.addGestureRecognizer(swipe)
 }
 
+@objc private func deleteWord() {
+    let proxy = textDocumentProxy
+    guard var before = proxy.documentContextBeforeInput else { return }
+    while before.last == " " { proxy.deleteBackward(); before.removeLast() }
+    while let c = before.last, c != " " { proxy.deleteBackward(); before.removeLast() }
+}
+
`,
	},
	"ios-app#258": {
		body:    "## 3.1\n\n- Faster startup on iPhone 12 and older\n- Fixes the keyboard sometimes losing the microphone after a phone call\n- New: swipe to delete a word\n\nBumps `MARKETING_VERSION` to 3.1 and build to 482.",
		commits: []string{"Bump version to 3.1 (482)", "Write 3.1 release notes"},
		review:  "Notes read well. Approved.",
		thread:  demoThread{path: "fastlane/metadata/en-US/release_notes.txt", line: 1, author: "sam", body: "Skip this one, the startup change didn't land in this branch.", reply: "Removed in the last commit.", resolved: true},
		patch: `diff --git a/Config/Version.xcconfig b/Config/Version.xcconfig
index 42bd36f..cec8fba 100644
--- a/Config/Version.xcconfig
+++ b/Config/Version.xcconfig
@@ -1,2 +1,2 @@
-MARKETING_VERSION = 3.0.2
-CURRENT_PROJECT_VERSION = 471
+MARKETING_VERSION = 3.1
+CURRENT_PROJECT_VERSION = 482
diff --git a/fastlane/metadata/en-US/release_notes.txt b/fastlane/metadata/en-US/release_notes.txt
index 13f3a89..724a7bc 100644
--- a/fastlane/metadata/en-US/release_notes.txt
+++ b/fastlane/metadata/en-US/release_notes.txt
@@ -1,1 +1,3 @@
-Bug fixes and improvements.
+- Faster startup on older iPhones
+- Fixed the keyboard losing the microphone after a call
+- New: swipe left on the keyboard to delete a word
`,
	},
	"ios-app#255": {
		body:     "Adds a search field to the emoji panel.\n\nType \"fire\" and you get the fire emoji and friends. Keywords come from the CLDR annotation data, bundled as a 180 KB JSON file.\n\nOpen question: do we want recents above results?",
		commits:  []string{"Bundle CLDR emoji keywords", "Add EmojiSearchIndex", "Add search field to the emoji panel", "Debounce search input"},
		comments: []demoComment{{"priya", "Searching 'heart' returns 40+ results in random order. Rank exact matches first?"}, {"jordan", "Good idea, will sort exact then prefix."}},
		review:   "Does the keyboard extension stay under the memory limit with the JSON loaded? Parsing 180 KB should be fine but I'd like a number.",
		thread:   demoThread{path: "Keyboard/Emoji/EmojiSearchIndex.swift", line: 8, author: "priya", body: "try! in a keyboard extension will crash the whole keyboard if the file is missing.", reply: "", resolved: false},
		patch: `diff --git a/Keyboard/Emoji/EmojiSearchIndex.swift b/Keyboard/Emoji/EmojiSearchIndex.swift
new file mode 100644
index 0000000..a733634
--- /dev/null
+++ b/Keyboard/Emoji/EmojiSearchIndex.swift
@@ -0,0 +1,19 @@
+import Foundation
+
+struct EmojiSearchIndex {
+    private let entries: [(emoji: String, words: [String])]
+
+    init(bundle: Bundle = .main) {
+        let url = bundle.url(forResource: "emoji_keywords", withExtension: "json")!
+        let raw = try! JSONDecoder().decode([String: [String]].self, from: Data(contentsOf: url))
+        entries = raw.map { ($0.key, $0.value) }
+    }
+
+    func search(_ query: String) -> [String] {
+        let q = query.lowercased().trimmingCharacters(in: .whitespaces)
+        guard !q.isEmpty else { return [] }
+        return entries
+            .filter { $0.words.contains { $0.hasPrefix(q) } }
+            .map(\.emoji)
+    }
+}
`,
	},
	"ios-app#251": {
		body:    "Light haptic on each key press, behind a setting. Off by default. Still need to check battery impact.",
		commits: []string{"Add KeyHaptics helper", "Add Haptics toggle in settings"},
		thread:  demoThread{path: "Keyboard/KeyHaptics.swift", line: 9, author: "jordan", body: "prepare() after every press keeps the Taptic Engine warm, which costs battery. Only prepare on touch down?", reply: "", resolved: false},
		patch: `diff --git a/Keyboard/KeyHaptics.swift b/Keyboard/KeyHaptics.swift
new file mode 100644
index 0000000..27d93e6
--- /dev/null
+++ b/Keyboard/KeyHaptics.swift
@@ -0,0 +1,17 @@
+import UIKit
+
+final class KeyHaptics {
+    private let generator = UIImpactFeedbackGenerator(style: .light)
+    var isEnabled = false
+
+    func prepare() {
+        guard isEnabled else { return }
+        generator.prepare()
+    }
+
+    func keyPressed() {
+        guard isEnabled else { return }
+        generator.impactOccurred()
+        generator.prepare()
+    }
+}
diff --git a/Settings/KeyboardSettingsView.swift b/Settings/KeyboardSettingsView.swift
index 5cc2369..e7dae5f 100644
--- a/Settings/KeyboardSettingsView.swift
+++ b/Settings/KeyboardSettingsView.swift
@@ -22,3 +22,4 @@ var body: some View {
     Form {
         Toggle("Auto-capitalize", isOn: $settings.autoCap)
+        Toggle("Haptic feedback", isOn: $settings.haptics)
     }
`,
	},
	"ios-app#249": {
		body:    "Dictation stopped after exactly 60s on iOS 17 because the audio session gets interrupted by the system. We now restart the session at 55s and stitch the audio.\n\nFixes #237",
		commits: []string{"Restart audio session at 55 seconds"},
		review:  "restartAudioSession drops about 200ms of audio at the seam, so words get cut at the 55s mark. Can we start the new session before stopping the old one and overlap them?",
		thread:  demoThread{path: "Dictation/DictationSession.swift", line: 88, author: "sarah", body: "Is the limit really fixed at 60s? Might be better to read the interruption notification than hard-code 55.", reply: "I couldn't find a notification, it just stops.", resolved: false},
		patch: `diff --git a/Dictation/DictationSession.swift b/Dictation/DictationSession.swift
index fc3c99e..8007cbd 100644
--- a/Dictation/DictationSession.swift
+++ b/Dictation/DictationSession.swift
@@ -88,2 +88,4 @@ func startTimer() {
-    // no limit handling
+    Timer.scheduledTimer(withTimeInterval: 55, repeats: true) { [weak self] _ in
+        self?.restartAudioSession()
+    }
 }
`,
	},
	"web-app#402": {
		body:     "The billing page now shows **Next invoice: Nov 14, $12.00**, using the upcoming invoice from Stripe.\n\n![Billing page with the next invoice row](demo:billing)\n\nFixes SU-1877. Customers kept emailing to ask when they would be charged.\n\nEdge cases handled:\n- No upcoming invoice (cancelled): section is hidden\n- Trial: shows \"Trial ends Nov 14\"\n- Proration lines are included in the total",
		commits:  []string{"Add /api/billing/upcoming route", "Add NextInvoice component", "Show trial end for trialing subscriptions"},
		comments: []demoComment{{"alex", "Currency formatting for JPY should have no decimals, does formatCurrency handle it?"}, {"taylor", "Uses Intl.NumberFormat so yes. Added a test."}},
		thread:   demoThread{path: "app/billing/NextInvoice.tsx", line: 13, author: "alex", body: "amountDue is in minor units from Stripe. Is formatCurrency dividing by 100?", reply: "Yes, and zero-decimal currencies are special-cased there.", resolved: true},
		patch: `diff --git a/app/billing/NextInvoice.tsx b/app/billing/NextInvoice.tsx
new file mode 100644
index 0000000..04e2a07
--- /dev/null
+++ b/app/billing/NextInvoice.tsx
@@ -0,0 +1,17 @@
+import { formatCurrency, formatDate } from "@/lib/format";
+
+type Upcoming = { amountDue: number; currency: string; date: string; trialing: boolean };
+
+export function NextInvoice({ upcoming }: { upcoming: Upcoming | null }) {
+  if (!upcoming) return null;
+  const when = formatDate(upcoming.date);
+  return (
+    <section className="next-invoice">
+      <h3>{upcoming.trialing ? "Trial ends" : "Next invoice"}</h3>
+      <p>
+        {when}
+        {!upcoming.trialing && <> &middot; {formatCurrency(upcoming.amountDue, upcoming.currency)}</>}
+      </p>
+    </section>
+  );
+}
diff --git a/app/billing/page.tsx b/app/billing/page.tsx
index df93aa8..e471f84 100644
--- a/app/billing/page.tsx
+++ b/app/billing/page.tsx
@@ -14,5 +14,7 @@ export default async function BillingPage() {
   const sub = await getSubscription();
+  const upcoming = sub ? await getUpcomingInvoice(sub.id) : null;
   return (
     <main>
       <PlanCard sub={sub} />
+      <NextInvoice upcoming={upcoming} />
       <PaymentMethod />
`,
	},
	"web-app#400": {
		body:    "Updates the refund policy from 7 days to 14 days (legal approved, SU-2301).",
		commits: []string{"Update refund window to 14 days", "Update FAQ and checkout copy"},
		review:  "Matches the approved text. Approved.",
		thread:  demoThread{path: "content/faq.mdx", line: 32, author: "mike", body: "The pricing page still says 7 days, grep for it.", reply: "Fixed in the second commit.", resolved: true},
		patch: `diff --git a/content/legal/refunds.mdx b/content/legal/refunds.mdx
index 65a3151..9fa1d89 100644
--- a/content/legal/refunds.mdx
+++ b/content/legal/refunds.mdx
@@ -5,4 +5,4 @@
-If you are not satisfied, contact us within **7 days** of your purchase and we will refund you in full.
+If you are not satisfied, contact us within **14 days** of your purchase and we will refund you in full.
 
 Refunds are returned to the original payment method.
-They usually appear within 5 to 10 business days.
+They usually appear within 5 to 10 business days, depending on your bank.
diff --git a/content/faq.mdx b/content/faq.mdx
index f80503b..d33b9aa 100644
--- a/content/faq.mdx
+++ b/content/faq.mdx
@@ -31,2 +31,2 @@
 ### Can I get a refund?
-Yes, within 7 days of purchase.
+Yes, within 14 days of purchase.
diff --git a/app/checkout/Guarantee.tsx b/app/checkout/Guarantee.tsx
index 4b4a729..f6dccd5 100644
--- a/app/checkout/Guarantee.tsx
+++ b/app/checkout/Guarantee.tsx
@@ -3,1 +3,1 @@
-  return <p>7-day money-back guarantee</p>;
+  return <p>14-day money-back guarantee</p>;
`,
	},
	"web-app#398": {
		body:     "## Summary\nLanding page for the meeting recorder, live at `/meetings`.\n\n![Landing page](demo:landing)\n\n## Sections\n1. Hero with the demo video (autoplay, muted, 14s loop)\n2. Three feature rows: record anything, speaker labels, summaries\n3. Comparison table\n4. FAQ (8 questions)\n5. Download CTA\n\n## Performance\nLighthouse 98 mobile. Hero video is 1.1 MB (AV1 with H.264 fallback), lazy loaded below the fold.\n\n## Not in this PR\nTranslations and the pricing section, both coming next.",
		commits:  []string{"Scaffold /meetings route", "Add hero and demo video", "Add feature rows", "Add comparison table", "Add FAQ with structured data", "Add download CTA and analytics events"},
		comments: []demoComment{{"taylor", "Page loads fast on my phone. The video poster has a visible jump when the video starts."}, {"sam", "Matched the poster to frame 0, should be gone."}, {"priya", "Copy on the comparison table needs legal review before we publish."}},
		thread:   demoThread{path: "app/meetings/Hero.tsx", line: 6, author: "taylor", body: "Respect prefers-reduced-motion here? Autoplaying video can bother people.", reply: "", resolved: false},
		patch: `diff --git a/app/meetings/page.tsx b/app/meetings/page.tsx
new file mode 100644
index 0000000..c13c37d
--- /dev/null
+++ b/app/meetings/page.tsx
@@ -0,0 +1,21 @@
+import type { Metadata } from "next";
+import { Hero } from "./Hero";
+import { Features } from "./Features";
+import { Faq } from "./Faq";
+import { DownloadCta } from "@/components/DownloadCta";
+
+export const metadata: Metadata = {
+  title: "Meeting recorder",
+  description: "Record, transcribe and summarize any meeting on your Mac.",
+};
+
+export default function MeetingsPage() {
+  return (
+    <>
+      <Hero />
+      <Features />
+      <Faq />
+      <DownloadCta source="meetings" />
+    </>
+  );
+}
diff --git a/app/meetings/Hero.tsx b/app/meetings/Hero.tsx
new file mode 100644
index 0000000..f64cb99
--- /dev/null
+++ b/app/meetings/Hero.tsx
@@ -0,0 +1,9 @@
+export function Hero() {
+  return (
+    <section className="meetings-hero">
+      <h1>Every meeting, written down</h1>
+      <p>Record any call on your Mac. Get a transcript with speakers and a summary in seconds.</p>
+      <video src="/video/meetings-demo.mp4" autoPlay muted loop playsInline poster="/img/meetings-poster.jpg" />
+    </section>
+  );
+}
diff --git a/components/Nav.tsx b/components/Nav.tsx
index ef3e4ec..f2545d1 100644
--- a/components/Nav.tsx
+++ b/components/Nav.tsx
@@ -21,2 +21,3 @@ const links = [
   { href: "/pricing", label: "Pricing" },
+  { href: "/meetings", label: "Meetings" },
   { href: "/docs", label: "Docs" },
`,
	},
	"web-app#395": {
		body:     "Admins can turn on **Join by email domain** for their team. Anyone signing up with an address at that domain is added automatically.\n\n### How it works\n1. Admin verifies the domain with a DNS TXT record\n2. `team_domains` stores the verified domain\n3. On signup we look up the domain and add the user as a member\n\nFree-mail domains (gmail etc) are blocked, see #408.\n\n### Test plan\n- [x] Unit tests for domain matching\n- [ ] DNS verification against a real domain",
		commits:  []string{"Add team_domains table and model", "Add domain settings UI", "Auto-join on signup when domain matches", "Verify domain ownership with DNS TXT", "Add tests for domain matching"},
		comments: []demoComment{{"sarah", "What happens when someone leaves the company? Their email still works for the domain, but we have no signal."}, {"chris", "Admins remove them manually for now."}},
		review:   "Anyone who can receive mail at the domain gets in. That includes catch-all addresses and shared aliases like info@ . Please require the user to verify their email before the auto-join runs.",
		thread:   demoThread{path: "lib/teams/domainJoin.ts", line: 13, author: "sarah", body: "Auto-joined users should land with the lowest role and no billing access. Is member that?", reply: "", resolved: false},
		patch: `diff --git a/lib/teams/domainJoin.ts b/lib/teams/domainJoin.ts
new file mode 100644
index 0000000..f7d87b7
--- /dev/null
+++ b/lib/teams/domainJoin.ts
@@ -0,0 +1,15 @@
+import { db } from "@/lib/db";
+import { isFreeMail } from "@/lib/freemail";
+
+export async function teamForEmail(email: string) {
+  const domain = email.split("@")[1]?.toLowerCase();
+  if (!domain || isFreeMail(domain)) return null;
+  return db.teamDomains.findFirst({ where: { domain, verified: true } });
+}
+
+export async function joinByDomain(userId: string, email: string) {
+  const match = await teamForEmail(email);
+  if (!match) return null;
+  await db.teamMembers.create({ data: { teamId: match.teamId, userId, role: "member" } });
+  return match.teamId;
+}
`,
	},
	"web-app#391": {
		body:     "Draft of dark mode for the account dashboard.\n\n![Light vs dark](demo:darkmode)\n\n- [x] Tokens for surfaces and text\n- [x] Charts\n- [ ] Billing page (Stripe elements need their own appearance config)\n- [ ] Settings forms\n- [ ] Review images and logos with transparent backgrounds",
		commits:  []string{"Add dashboard color tokens", "Apply tokens to layout and cards", "Theme the usage charts"},
		comments: []demoComment{{"priya", "Charts look good. The legend text is still black in dark."}},
		thread:   demoThread{path: "app/dashboard/tokens.css", line: 11, author: "priya", body: "#1a1d23 on #101216 is hard to tell apart on cheap monitors. Try a slightly lighter raised surface.", reply: "", resolved: false},
		patch: `diff --git a/app/dashboard/tokens.css b/app/dashboard/tokens.css
new file mode 100644
index 0000000..2d406a7
--- /dev/null
+++ b/app/dashboard/tokens.css
@@ -0,0 +1,15 @@
+.dashboard {
+  --surface: #ffffff;
+  --surface-raised: #f6f7f9;
+  --text: #14171c;
+  --border: #e2e5ea;
+}
+
+@media (prefers-color-scheme: dark) {
+  .dashboard {
+    --surface: #101216;
+    --surface-raised: #1a1d23;
+    --text: #e9ebef;
+    --border: #2a2e36;
+  }
+}
diff --git a/app/dashboard/Card.module.css b/app/dashboard/Card.module.css
index efe39ab..5f08387 100644
--- a/app/dashboard/Card.module.css
+++ b/app/dashboard/Card.module.css
@@ -1,5 +1,6 @@
 .card {
-  background: #fff;
-  border: 1px solid #e2e5ea;
+  background: var(--surface-raised);
+  border: 1px solid var(--border);
   border-radius: 12px;
+  color: var(--text);
 }
`,
	},
	"backend-api#411": {
		body:     "## Summary\nWhen a subscription is cancelled inside the refund window (14 days from the latest charge), refund that charge automatically and say so in the cancellation email.\n\n## Flow\n1. `CancelSubscription` loads the latest paid invoice\n2. If `now - paid_at <= RefundWindow` we call Stripe refunds with an idempotency key\n3. We record a `refunds` row and send the email\n\n## Risks\nDouble refunds. The idempotency key is `cancel-refund-<subscription_id>` so retries are safe. Partial refunds (credits applied) refund only the amount actually charged.\n\n## Test plan\n- Unit tests with a fake Stripe client\n- Stripe test mode: cancel at day 1, day 14, day 15",
		commits:  []string{"Add RefundWindow config", "Add refunds table", "Refund latest charge in CancelSubscription", "Use idempotency key for refunds", "Only refund the charged amount, not credits", "Mention refund in cancellation email"},
		comments: []demoComment{{"taylor", "Does this cover annual plans? 14 days of an annual charge is a big refund."}, {"alex", "Yes, same window. Finance confirmed in SU-2288."}, {"mike", "UNIQUE on subscription_id means a re-subscribe and second cancel can never refund. Fine?"}, {"alex", "Hmm, no. Will key on the invoice instead."}},
		thread:   demoThread{path: "internal/billing/cancel.go", line: 52, author: "mike", body: "If the refund fails we have already cancelled. The user gets no refund and we return an error. Should refund come first?", reply: "", resolved: false},
		patch: `diff --git a/internal/billing/cancel.go b/internal/billing/cancel.go
index a5a36b2..14089b1 100644
--- a/internal/billing/cancel.go
+++ b/internal/billing/cancel.go
@@ -52,5 +52,16 @@ func (s *Service) CancelSubscription(ctx context.Context, id string) error {
 	if err := s.stripe.Cancel(ctx, id); err != nil {
 		return err
 	}
+	inv, err := s.store.LatestPaidInvoice(ctx, id)
+	if err != nil {
+		return err
+	}
+	if inv != nil && time.Since(inv.PaidAt) <= s.cfg.RefundWindow {
+		amount := inv.AmountPaid - inv.CreditApplied
+		if _, err := s.stripe.Refund(ctx, inv.ChargeID, amount, "cancel-refund-"+id); err != nil {
+			return fmt.Errorf("refund: %w", err)
+		}
+		return s.store.RecordRefund(ctx, id, inv.ChargeID, amount)
+	}
 	return nil
 }
diff --git a/migrations/0058_refunds.sql b/migrations/0058_refunds.sql
new file mode 100644
index 0000000..3598f4f
--- /dev/null
+++ b/migrations/0058_refunds.sql
@@ -0,0 +1,8 @@
+CREATE TABLE refunds (
+    id              BIGSERIAL PRIMARY KEY,
+    subscription_id TEXT NOT NULL,
+    charge_id       TEXT NOT NULL,
+    amount_cents    BIGINT NOT NULL,
+    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
+    UNIQUE (subscription_id)
+);
`,
	},
	"backend-api#410": {
		body:     "When a member is removed or leaves, their seat is released and their license is disabled immediately, not at the next nightly sync.\n\nFixes SU-2110.",
		commits:  []string{"Add ReleaseSeat to org store", "Call ReleaseSeat from RemoveMember", "Test seat release on leave"},
		comments: []demoComment{{"taylor", "Tested manually, seat count updates right away in the dashboard."}},
		review:   "Simple and well tested. Approved.",
		thread:   demoThread{path: "internal/orgs/members.go", line: 80, author: "alex", body: "These three steps are not in one transaction. If Disable fails the member is gone but still licensed.", reply: "True, but the nightly job cleans up leftovers. Wrapping in a tx is a follow-up.", resolved: true},
		patch: `diff --git a/internal/orgs/members.go b/internal/orgs/members.go
index 6ad2176..a7b209b 100644
--- a/internal/orgs/members.go
+++ b/internal/orgs/members.go
@@ -77,5 +77,11 @@ func (s *Service) RemoveMember(ctx context.Context, orgID, userID int64) error {
 	if err := s.store.DeleteMember(ctx, orgID, userID); err != nil {
 		return err
 	}
+	if err := s.licenses.Disable(ctx, userID, orgID); err != nil {
+		return fmt.Errorf("disable license: %w", err)
+	}
+	if err := s.store.ReleaseSeat(ctx, orgID); err != nil {
+		return fmt.Errorf("release seat: %w", err)
+	}
 	return nil
 }
diff --git a/internal/orgs/members_test.go b/internal/orgs/members_test.go
index 0e45f68..e26416e 100644
--- a/internal/orgs/members_test.go
+++ b/internal/orgs/members_test.go
@@ -40,7 +40,10 @@
 func TestRemoveMember(t *testing.T) {
 	s := newTestService(t)
 	s.AddMember(ctx, 1, 7)
 	if err := s.RemoveMember(ctx, 1, 7); err != nil {
 		t.Fatal(err)
 	}
+	if got := s.store.SeatsUsed(ctx, 1); got != 0 {
+		t.Fatalf("seats used = %d, want 0", got)
+	}
 }
`,
	},
	"backend-api#409": {
		body:     "Org trials used to keep their `trial_ends_at` after the customer upgraded, so a paid org could still be expired by the nightly job. Upgrade now sets it to NULL.\n\n- Adds a migration to fix the 23 orgs affected so far (list in SU-2180)\n- Adds a regression test",
		commits:  []string{"Clear trial_ends_at on upgrade", "Add migration to repair affected orgs", "Add regression test"},
		comments: []demoComment{{"taylor", "The migration is a bit broad. Limit to orgs with an active subscription?"}},
		thread:   demoThread{path: "migrations/0057_clear_trial_expiry.sql", line: 3, author: "taylor", body: "Free-plan orgs also have plan <> 'trial'. They never had a trial expiry, but I'd add AND subscription_id IS NOT NULL to be safe.", reply: "", resolved: false},
		patch: `diff --git a/internal/orgs/upgrade.go b/internal/orgs/upgrade.go
index 71a9e3a..a4698f3 100644
--- a/internal/orgs/upgrade.go
+++ b/internal/orgs/upgrade.go
@@ -29,3 +29,4 @@ func (s *Service) Upgrade(ctx context.Context, orgID int64, plan string) error {
 	org.Plan = plan
+	org.TrialEndsAt = nil
 	return s.store.SaveOrg(ctx, org)
 }
diff --git a/migrations/0057_clear_trial_expiry.sql b/migrations/0057_clear_trial_expiry.sql
new file mode 100644
index 0000000..79c3917
--- /dev/null
+++ b/migrations/0057_clear_trial_expiry.sql
@@ -0,0 +1,4 @@
+UPDATE orgs
+SET trial_ends_at = NULL
+WHERE plan <> 'trial'
+  AND trial_ends_at IS NOT NULL;
`,
	},
	"backend-api#408": {
		body:    "Adds 40 free-mail providers (regional ones mostly) to the blocklist used when checking team signups. Also sorts the list and removes duplicates.",
		commits: []string{"Add 40 free-mail domains", "Sort and dedupe the list"},
		review:  "Thanks for the dedupe. LGTM.",
		thread:  demoThread{path: "internal/freemail/domains.go", line: 16, author: "taylor", body: "hushmail is a paid privacy mail service, some companies do use it. Keep?", reply: "Kept, it's free for personal use too.", resolved: true},
		patch: `diff --git a/internal/freemail/domains.go b/internal/freemail/domains.go
index 23461bc..00845d5 100644
--- a/internal/freemail/domains.go
+++ b/internal/freemail/domains.go
@@ -10,8 +10,16 @@ var Domains = map[string]struct{}{
 	"aol.com":        {},
+	"fastmail.fm":    {},
 	"gmail.com":      {},
+	"gmx.de":         {},
+	"gmx.net":        {},
 	"hotmail.com":    {},
-	"hotmail.com":    {},
+	"hushmail.com":   {},
 	"icloud.com":     {},
+	"libero.it":      {},
+	"mail.ru":        {},
 	"outlook.com":    {},
+	"seznam.cz":      {},
+	"wp.pl":          {},
 	"yahoo.com":      {},
+	"yandex.com":     {},
 	"zoho.com":       {},
`,
	},
	"backend-api#404": {
		body:     "## Summary\nA nightly job (03:00 UTC) expires organization licenses for orgs whose trial ended and who never added a payment method.\n\n## Behavior\n- Selects orgs where `trial_ends_at < now()` and no `payment_method_id`\n- Disables licenses in batches of 100\n- Sends a heads-up email 3 days before\n\n## Rollout\nBehind `ORG_TRIAL_EXPIRY=1`. I'd like to run it dry first and compare against the list in SU-2203.",
		commits:  []string{"Add ExpiredTrials query", "Add nightly expiry job", "Add 3 day warning email", "Add dry-run mode"},
		comments: []demoComment{{"mike", "How many orgs would the first run hit?"}, {"taylor", "Dry run on staging: 112. Prod should be similar, I'll post the number when it's run."}},
		review:   "One question on the loop: if DisableOrg fails halfway through a batch we return, but orgs already disabled drop out of ExpiredTrials. And if it succeeds but the licenses_enabled flag isn't updated, the loop never ends. Where is the flag cleared?",
		thread:   demoThread{path: "internal/jobs/expire_trials.go", line: 11, author: "mike", body: "Infinite loop if the query keeps returning the same orgs. Add an upper bound on iterations.", reply: "", resolved: false},
		patch: `diff --git a/internal/jobs/expire_trials.go b/internal/jobs/expire_trials.go
new file mode 100644
index 0000000..01b5503
--- /dev/null
+++ b/internal/jobs/expire_trials.go
@@ -0,0 +1,32 @@
+package jobs
+
+import (
+	"context"
+	"log/slog"
+)
+
+const expireBatch = 100
+
+func ExpireTrials(ctx context.Context, d Deps, dryRun bool) error {
+	for {
+		orgs, err := d.Store.ExpiredTrials(ctx, expireBatch)
+		if err != nil {
+			return err
+		}
+		if len(orgs) == 0 {
+			return nil
+		}
+		for _, o := range orgs {
+			slog.Info("expiring org trial", "org", o.ID, "dry_run", dryRun)
+			if dryRun {
+				continue
+			}
+			if err := d.Licenses.DisableOrg(ctx, o.ID); err != nil {
+				return err
+			}
+		}
+		if dryRun {
+			return nil
+		}
+	}
+}
diff --git a/internal/store/orgs.go b/internal/store/orgs.go
index 0128c16..08e879a 100644
--- a/internal/store/orgs.go
+++ b/internal/store/orgs.go
@@ -88,1 +88,9 @@
+func (s *Store) ExpiredTrials(ctx context.Context, limit int) ([]Org, error) {
+	rows, err := s.db.Query(ctx, "SELECT id FROM orgs WHERE trial_ends_at < now() AND payment_method_id IS NULL AND licenses_enabled LIMIT $1", limit)
+	if err != nil {
+		return nil, err
+	}
+	return pgx.CollectRows(rows, pgx.RowToStructByName[Org])
+}
+
 func (s *Store) SaveOrg(ctx context.Context, o *Org) error {
`,
	},
	"backend-api#399": {
		body:    "Draft. Per-plan limits for `/v1/transcribe`:\n\n| Plan | Requests/min | Concurrent |\n|------|-------------|------------|\n| Free | 10 | 1 |\n| Pro | 60 | 4 |\n| Team | 300 | 16 |\n\nBuilds on #445. Don't merge before that lands.",
		commits: []string{"Add plan limit table", "Look up limits by plan in transcribe handler"},
		thread:  demoThread{path: "internal/ratelimit/plans.go", line: 9, author: "alex", body: "Unknown plans fall back to free. Enterprise customers on a custom plan would get throttled. Fail open or add enterprise?", reply: "", resolved: false},
		patch: `diff --git a/internal/ratelimit/plans.go b/internal/ratelimit/plans.go
new file mode 100644
index 0000000..f350ded
--- /dev/null
+++ b/internal/ratelimit/plans.go
@@ -0,0 +1,19 @@
+package ratelimit
+
+type Limit struct {
+	PerMinute  int
+	Concurrent int
+}
+
+var PlanLimits = map[string]Limit{
+	"free": {PerMinute: 10, Concurrent: 1},
+	"pro":  {PerMinute: 60, Concurrent: 4},
+	"team": {PerMinute: 300, Concurrent: 16},
+}
+
+func ForPlan(plan string) Limit {
+	if l, ok := PlanLimits[plan]; ok {
+		return l
+	}
+	return PlanLimits["free"]
+}
diff --git a/internal/http/transcribe.go b/internal/http/transcribe.go
index 071abef..36ff33a 100644
--- a/internal/http/transcribe.go
+++ b/internal/http/transcribe.go
@@ -33,2 +33,8 @@ func (h *Handler) Transcribe(w http.ResponseWriter, r *http.Request) {
 	user := userFrom(r.Context())
+	lim := ratelimit.ForPlan(user.Plan)
+	if !h.limiter.Allow(user.ID, lim) {
+		w.Header().Set("Retry-After", "60")
+		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
+		return
+	}
 	audio, err := readAudio(r)
`,
	},
	"job-runner#293": {
		body:     "## What\nFailed jobs retry up to 5 times with exponential backoff (1s, 2s, 4s, 8s, 16s, plus up to 20% jitter) before moving to the dead letter queue.\n\n## Why\nTransient errors from the transcription provider were failing jobs that would succeed seconds later.\n\n## Config\n`JOB_MAX_ATTEMPTS` (default 5), `JOB_BACKOFF_BASE` (default 1s).",
		commits:  []string{"Add attempts column to jobs", "Add backoff calculation with jitter", "Re-enqueue failed jobs with delay", "Move to dead letter queue after max attempts"},
		comments: []demoComment{{"jordan", "rand.Int63n panics when its argument is 0. Is base ever 0?"}, {"alex", "Only if misconfigured. Added a guard in config validation."}},
		thread:   demoThread{path: "internal/retry/backoff.go", line: 9, author: "jordan", body: "Shift overflows for large attempt counts. Cap it?", reply: "Max attempts is 5 so it can't get there, but I'll add a cap anyway.", resolved: true},
		patch: `diff --git a/internal/retry/backoff.go b/internal/retry/backoff.go
new file mode 100644
index 0000000..e484e5c
--- /dev/null
+++ b/internal/retry/backoff.go
@@ -0,0 +1,12 @@
+package retry
+
+import (
+	"math/rand"
+	"time"
+)
+
+func Backoff(attempt int, base time.Duration) time.Duration {
+	d := base << attempt
+	jitter := time.Duration(rand.Int63n(int64(d) / 5))
+	return d + jitter
+}
diff --git a/internal/worker/worker.go b/internal/worker/worker.go
index d501b2f..869ab57 100644
--- a/internal/worker/worker.go
+++ b/internal/worker/worker.go
@@ -64,2 +64,7 @@ func (w *Worker) handleFailure(ctx context.Context, j *Job, err error) {
-	w.queue.Fail(ctx, j, err)
+	j.Attempts++
+	if j.Attempts >= w.cfg.MaxAttempts {
+		w.queue.DeadLetter(ctx, j, err)
+		return
+	}
+	w.queue.RetryAt(ctx, j, time.Now().Add(retry.Backoff(j.Attempts-1, w.cfg.BackoffBase)))
 }
`,
	},
	"job-runner#290": {
		body:     "Workers now stop taking new jobs on SIGTERM and wait for the current one to finish (up to `SHUTDOWN_GRACE`, default 30s) before exiting.\n\nBefore this, a deploy killed in-flight transcriptions and they had to be retried from the start.",
		commits:  []string{"Stop dequeuing on SIGTERM", "Wait for in-flight jobs with a timeout", "Add shutdown test"},
		comments: []demoComment{{"sarah", "Does k8s terminationGracePeriodSeconds need to go up to 45?"}},
		review:   "LGTM. Please bump terminationGracePeriodSeconds in the same release.",
		thread:   demoThread{path: "cmd/worker/main.go", line: 31, author: "sarah", body: "If the grace period expires the job is lost unless the queue visibility timeout puts it back. It does, right?", reply: "Yes, visibility timeout is 60s.", resolved: true},
		patch: `diff --git a/cmd/worker/main.go b/cmd/worker/main.go
index f97a0f4..7786b3a 100644
--- a/cmd/worker/main.go
+++ b/cmd/worker/main.go
@@ -22,2 +22,13 @@ func main() {
-	w.Run(context.Background())
+	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
+	defer stop()
+	go w.Run(ctx)
+	<-ctx.Done()
+	slog.Info("shutting down, waiting for current job")
+	done := make(chan struct{})
+	go func() { w.Wait(); close(done) }()
+	select {
+	case <-done:
+	case <-time.After(cfg.ShutdownGrace):
+		slog.Warn("grace period expired, exiting with a job in flight")
+	}
 }
`,
	},
	"data-service#160": {
		body:     "Exports yesterday's usage to the warehouse every day at 02:00 UTC as a gzipped CSV in the `usage/` bucket prefix.\n\n**Columns:** `day, user_id, org_id, seconds_dictated, words, meeting_seconds`\n\nNotes:\n- Idempotent: re-running a day overwrites the same object\n- Uses a read replica so it does not compete with the API\n- Metrics: `usage_export_rows`, `usage_export_seconds`",
		commits:  []string{"Add usage rollup query", "Write rows to gzipped CSV", "Upload to warehouse bucket", "Schedule at 02:00 UTC", "Export metrics"},
		comments: []demoComment{{"alex", "Is 02:00 enough after the 01:00 rollup, how long does the rollup take?"}, {"priya", "About 6 minutes on prod data, plenty of room."}},
		thread:   demoThread{path: "internal/export/usage.go", line: 30, author: "chris", body: "cw.Write returns an error that is ignored here. cw.Error() catches it at the end, but only for the last flush, so fine. Just noting.", reply: "Added a comment so nobody trips on it.", resolved: true},
		patch: `diff --git a/internal/export/usage.go b/internal/export/usage.go
new file mode 100644
index 0000000..c05a4f6
--- /dev/null
+++ b/internal/export/usage.go
@@ -0,0 +1,35 @@
+package export
+
+import (
+	"compress/gzip"
+	"context"
+	"encoding/csv"
+	"fmt"
+	"io"
+	"time"
+)
+
+const usageQuery = "SELECT day, user_id, org_id, seconds_dictated, words, meeting_seconds FROM usage_daily WHERE day = $1"
+
+func WriteUsage(ctx context.Context, q Querier, day time.Time, w io.Writer) (int, error) {
+	gz := gzip.NewWriter(w)
+	defer gz.Close()
+	cw := csv.NewWriter(gz)
+	rows, err := q.Query(ctx, usageQuery, day.Format("2006-01-02"))
+	if err != nil {
+		return 0, err
+	}
+	defer rows.Close()
+	n := 0
+	for rows.Next() {
+		var d string
+		var user, org, secs, words, meet int64
+		if err := rows.Scan(&d, &user, &org, &secs, &words, &meet); err != nil {
+			return n, err
+		}
+		cw.Write([]string{d, fmt.Sprint(user), fmt.Sprint(org), fmt.Sprint(secs), fmt.Sprint(words), fmt.Sprint(meet)})
+		n++
+	}
+	cw.Flush()
+	return n, cw.Error()
+}
diff --git a/cmd/data-service/schedule.go b/cmd/data-service/schedule.go
index a60e156..93b0ea5 100644
--- a/cmd/data-service/schedule.go
+++ b/cmd/data-service/schedule.go
@@ -18,2 +18,3 @@ func schedule(s *cron.Cron, d Deps) {
 	s.AddFunc("0 1 * * *", d.RollupUsage)
+	s.AddFunc("0 2 * * *", d.ExportUsage)
 	s.AddFunc("*/5 * * * *", d.Heartbeat)
`,
	},
	"data-service#158": {
		body:     "Drops `events_legacy`. Everything now reads from the partitioned `events` table (see #142).\n\n- Verified no queries touch it in the last 30 days (pg_stat_user_tables)\n- Backup taken: `s3://acme-backups/events_legacy_2026-10-28.dump`\n- Removes the Go reader and the writer shim\n\n**This is irreversible without the backup.**",
		commits:  []string{"Remove legacy events reader", "Remove dual-write shim", "Drop events_legacy table"},
		comments: []demoComment{{"sarah", "Please wait until after the Friday release, I don't want two risky changes in one day."}},
		review:   "This can't merge yet. The dashboard export in internal/export still reads events_legacy for accounts older than 2024 (grep for LegacyEvents in cmd/). Remove or migrate that first.",
		thread:   demoThread{path: "migrations/0059_drop_events_legacy.sql", line: 1, author: "sarah", body: "Rename to events_legacy_old first and drop it a week later. Easier to undo than restoring a dump.", reply: "", resolved: false},
		patch: `diff --git a/internal/store/events_legacy.go b/internal/store/events_legacy.go
deleted file mode 100644
index 10564cc..0000000
--- a/internal/store/events_legacy.go
+++ /dev/null
@@ -1,28 +0,0 @@
-package store
-
-import "context"
-
-// LegacyEvent is the pre-partitioning row shape.
-type LegacyEvent struct {
-	ID        int64
-	UserID    int64
-	Kind      string
-	CreatedAt int64
-}
-
-func (s *Store) LegacyEvents(ctx context.Context, userID int64) ([]LegacyEvent, error) {
-	rows, err := s.db.Query(ctx, "SELECT id, user_id, kind, created_at FROM events_legacy WHERE user_id = $1", userID)
-	if err != nil {
-		return nil, err
-	}
-	defer rows.Close()
-	var out []LegacyEvent
-	for rows.Next() {
-		var e LegacyEvent
-		if err := rows.Scan(&e.ID, &e.UserID, &e.Kind, &e.CreatedAt); err != nil {
-			return nil, err
-		}
-		out = append(out, e)
-	}
-	return out, rows.Err()
-}
diff --git a/migrations/0059_drop_events_legacy.sql b/migrations/0059_drop_events_legacy.sql
new file mode 100644
index 0000000..451024c
--- /dev/null
+++ b/migrations/0059_drop_events_legacy.sql
@@ -0,0 +1,1 @@
+DROP TABLE IF EXISTS events_legacy;
diff --git a/internal/store/events.go b/internal/store/events.go
index 432e2e6..bf8e3cb 100644
--- a/internal/store/events.go
+++ b/internal/store/events.go
@@ -31,6 +31,3 @@ func (s *Store) InsertEvent(ctx context.Context, e Event) error {
 	_, err := s.db.Exec(ctx, insertEvent, e.UserID, e.Kind, e.CreatedAt)
-	if err == nil {
-		s.db.Exec(ctx, insertEventLegacy, e.UserID, e.Kind, e.CreatedAt)
-	}
 	return err
 }
`,
	},
	"cli-tools#207": {
		body:     "Adds a global `--json` flag. When set, commands print a single JSON object (or an array for lists) to stdout and send logs to stderr.\n\n```\n$ acme usage --json\n{\"words\": 18342, \"minutes\": 211, \"range\": \"30d\"}\n```\n\nCommands covered: `login`, `whoami`, `usage`, `transcribe`, `models list`, `config get`.\n\nErrors are `{\"error\": \"...\"}` with exit code 1.",
		commits:  []string{"Add output package with Print helper", "Add --json persistent flag to root command", "Convert usage and whoami", "Convert models and config commands", "Print errors as JSON when --json is set"},
		comments: []demoComment{{"sam", "Nice. Can the flag also come from ACME_JSON=1 for CI?"}, {"alex", "Added in the last commit."}},
		thread:   demoThread{path: "internal/output/output.go", line: 16, author: "sam", body: "SetIndent when stdout is a terminal? Easier to read by hand.", reply: "", resolved: false},
		patch: `diff --git a/internal/output/output.go b/internal/output/output.go
new file mode 100644
index 0000000..a3f2302
--- /dev/null
+++ b/internal/output/output.go
@@ -0,0 +1,20 @@
+package output
+
+import (
+	"encoding/json"
+	"fmt"
+	"io"
+)
+
+type Printer struct {
+	Out  io.Writer
+	JSON bool
+}
+
+func (p Printer) Print(v any, text string) error {
+	if p.JSON {
+		return json.NewEncoder(p.Out).Encode(v)
+	}
+	_, err := fmt.Fprintln(p.Out, text)
+	return err
+}
diff --git a/cmd/usage.go b/cmd/usage.go
index 920949d..cc6216b 100644
--- a/cmd/usage.go
+++ b/cmd/usage.go
@@ -24,7 +24,6 @@ func runUsage(cmd *cobra.Command, args []string) error {
 	u, err := client.Usage(ctx, rng)
 	if err != nil {
 		return err
 	}
-	fmt.Printf("%d words, %d minutes (%s)\n", u.Words, u.Minutes, rng)
-	return nil
+	return printer.Print(u, fmt.Sprintf("%d words, %d minutes (%s)", u.Words, u.Minutes, rng))
 }
`,
	},
	"cli-tools#205": {
		body:    "`acme completions zsh` and `acme completions fish` print a completion script.\n\nInstall:\n```\nacme completions zsh > ~/.zfunc/_acme\nacme completions fish > ~/.config/fish/completions/acme.fish\n```\nCompletes subcommands, flags, and model names (fetched from the API with a 500ms timeout).",
		commits: []string{"Add completions command", "Complete model names dynamically", "Add completion docs to README"},
		review:  "Works in both shells. Ship it.",
		thread:  demoThread{path: "cmd/completions.go", line: 20, author: "alex", body: "Bash would be easy too with GenBashCompletionV2. Skip for now, or add?", reply: "Out of scope for this PR, happy to add later.", resolved: true},
		patch: `diff --git a/cmd/completions.go b/cmd/completions.go
new file mode 100644
index 0000000..66327fb
--- /dev/null
+++ b/cmd/completions.go
@@ -0,0 +1,28 @@
+package cmd
+
+import (
+	"fmt"
+	"os"
+
+	"github.com/spf13/cobra"
+)
+
+var completionsCmd = &cobra.Command{
+	Use:       "completions [zsh|fish]",
+	Short:     "Print a shell completion script",
+	Args:      cobra.ExactArgs(1),
+	ValidArgs: []string{"zsh", "fish"},
+	RunE: func(cmd *cobra.Command, args []string) error {
+		switch args[0] {
+		case "zsh":
+			return rootCmd.GenZshCompletion(os.Stdout)
+		case "fish":
+			return rootCmd.GenFishCompletion(os.Stdout, true)
+		}
+		return fmt.Errorf("unsupported shell %q", args[0])
+	},
+}
+
+func init() {
+	rootCmd.AddCommand(completionsCmd)
+}
diff --git a/README.md b/README.md
index 04c6e90..faac267 100644
--- a/README.md
+++ b/README.md
@@ -31,3 +31,7 @@ ## Install
 Download the latest release from the releases page and put it on your PATH.
 
+### Shell completions
+
+Run acme completions zsh or acme completions fish and save the output where your shell looks for completions.
+
 ## Usage
`,
	},
	"docs#88": {
		body:     "## New page: Meeting recorder\n\nCovers:\n- Starting and stopping a recording\n- How speaker labels work, and renaming speakers\n- Summaries and action items\n- Exporting to Markdown, SRT and PDF\n- Privacy: where audio is stored\n\nNeeds screenshots once the 3.0 UI is final (placeholders marked `TODO`).",
		commits:  []string{"Outline the meetings guide", "Write recording and speakers sections", "Add export section", "Add privacy notes"},
		comments: []demoComment{{"sam", "Can we say how long audio is kept?"}, {"taylor", "30 days unless you save the recording. Added to privacy."}},
		thread:   demoThread{path: "docs/meetings/overview.mdx", line: 10, author: "sam", body: "Is the shortcut configurable? Worth one sentence.", reply: "", resolved: false},
		patch: `diff --git a/docs/meetings/overview.mdx b/docs/meetings/overview.mdx
new file mode 100644
index 0000000..9008e68
--- /dev/null
+++ b/docs/meetings/overview.mdx
@@ -0,0 +1,24 @@
+---
+title: "Meeting recorder"
+description: "Record, transcribe and summarize meetings."
+---
+
+The meeting recorder captures your microphone and system audio together, so you get both sides of a call.
+
+## Start a recording
+
+Press **Option + M** or choose **Record meeting** from the menu bar. A small window shows the timer and input levels.
+
+## Speakers
+
+Speakers are detected automatically and labeled **Speaker 1**, **Speaker 2** and so on. Click a label to rename it everywhere in the transcript.
+
+## Export
+
+| Format | Best for |
+|--------|----------|
+| Markdown | Notes apps |
+| SRT | Subtitles |
+| PDF | Sharing |
+
+{/* TODO: screenshots */}
diff --git a/docs/mint.json b/docs/mint.json
index 21d53f5..a0fd62c 100644
--- a/docs/mint.json
+++ b/docs/mint.json
@@ -21,4 +21,5 @@
   "navigation": [
     "quickstart",
+    "meetings/overview",
     "shortcuts"
   ]
`,
	},
	"docs#86": {
		body:    "Fixes three broken links found by the weekly link checker: the old pricing URL, the download page, and a typo in the changelog anchor.",
		commits: []string{"Fix three broken links"},
		review:  "Links all resolve now. Approved.",
		thread:  demoThread{path: "docs/changelog.mdx", line: 4, author: "priya", body: "Should we add the link checker to CI so this doesn't wait for the weekly run?", reply: "Good idea, separate PR.", resolved: true},
		patch: `diff --git a/docs/quickstart.mdx b/docs/quickstart.mdx
index b79cc7c..dccb171 100644
--- a/docs/quickstart.mdx
+++ b/docs/quickstart.mdx
@@ -12,1 +12,1 @@
-Download the app from [the download page](https://acme.example/dwonload).
+Download the app from [the download page](https://acme.example/download).
diff --git a/docs/billing.mdx b/docs/billing.mdx
index ef8b789..d6ee9b4 100644
--- a/docs/billing.mdx
+++ b/docs/billing.mdx
@@ -7,1 +7,1 @@
-See [pricing](https://acme.example/plans) for details.
+See [pricing](https://acme.example/pricing) for details.
diff --git a/docs/changelog.mdx b/docs/changelog.mdx
index 930fff7..6ecc60a 100644
--- a/docs/changelog.mdx
+++ b/docs/changelog.mdx
@@ -4,1 +4,1 @@
-Details in the [3.0 notes](#release-3-o).
+Details in the [3.0 notes](#release-3-0).
`,
	},
	"docs#84": {
		body:     "Rewrote the quickstart. It was 9 screens long, now it's 3 steps:\n\n1. Install\n2. Your first dictation\n3. Shortcuts worth learning\n\nThe old content moved to **Guides** where it belongs. Redirects added for the old anchors.",
		commits:  []string{"Cut quickstart down to install and first dictation", "Move permissions details to a guide", "Add shortcut cheat sheet", "Add redirects for old anchors"},
		comments: []demoComment{{"mike", "Much better. The old one lost me at 'modes'."}, {"priya", "Thanks! Sending the redirect list separately."}},
		review:   "Could we keep a link to troubleshooting at the end? People get stuck on the accessibility permission.",
		thread:   demoThread{path: "docs/quickstart.mdx", line: 20, author: "mike", body: "Is double-tap Fn the default in 3.0? I thought it changed.", reply: "", resolved: false},
		patch: `diff --git a/docs/quickstart.mdx b/docs/quickstart.mdx
index b79cc7c..dccb171 100644
--- a/docs/quickstart.mdx
+++ b/docs/quickstart.mdx
@@ -1,11 +1,21 @@
 ---
 title: "Quickstart"
 ---
 
-Welcome! This guide walks you through everything you need to know to get started, including account setup, permissions, models, modes, vocabulary and more.
+Get dictating in under two minutes.
 
-## Before you begin
-
-You will need a Mac running macOS 13 or later, an internet connection for the first launch, and administrator access for installing the app.
-
-## Step 1
+## 1. Install
+
+Download the app, drag it to Applications and open it. Allow microphone and accessibility access when asked.
+
+## 2. Dictate
+
+Hold **Fn**, say a sentence, release. The text appears where your cursor is.
+
+## 3. Learn three shortcuts
+
+| Shortcut | Action |
+|----------|--------|
+| Fn | Dictate while held |
+| Fn Fn | Toggle hands-free |
+| Option + Space | Open the mode picker |
`,
	},
	"docs#81": {
		body:    "Draft: reference docs for the organization endpoints.\n\n- [x] `GET /v1/orgs/{id}`\n- [x] `GET /v1/orgs/{id}/members`\n- [ ] `POST /v1/orgs/{id}/members`\n- [ ] `DELETE /v1/orgs/{id}/members/{user_id}`\n- [ ] Error codes table\n- [ ] Rate limits",
		commits: []string{"Add organizations API overview", "Document GET org and list members"},
		thread:  demoThread{path: "docs/api/organizations.mdx", line: 15, author: "taylor", body: "Add what happens when seats_used equals seats: do invites fail?", reply: "Yes, with 409. Adding to the error table.", resolved: false},
		patch: `diff --git a/docs/api/organizations.mdx b/docs/api/organizations.mdx
new file mode 100644
index 0000000..3c27ebf
--- /dev/null
+++ b/docs/api/organizations.mdx
@@ -0,0 +1,26 @@
+---
+title: "Organizations"
+---
+
+## Get an organization
+
+` + "`" + `GET /v1/orgs/{id}` + "`" + `
+
+` + "`" + `` + "`" + `` + "`" + `json
+{
+  "id": 42,
+  "name": "Acme Inc",
+  "plan": "team",
+  "seats": 25,
+  "seats_used": 18
+}
+` + "`" + `` + "`" + `` + "`" + `
+
+## List members
+
+` + "`" + `GET /v1/orgs/{id}/members` + "`" + `
+
+| Parameter | Type | Description |
+|-----------|------|-------------|
+| limit | integer | Page size, max 100 (default 50) |
+| cursor | string | From ` + "`" + `next_cursor` + "`" + ` of the previous page |
`,
	},
}
