package runtime

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"openpass/internal/l2tp"
)

// reviewRouteKernel models only observable kernel state relevant to recovery:
// exact rule multiplicity and a table's live/terminal default routes. Errors
// are injected before arbitrary mutations to represent interrupted applies.
type reviewRouteKernel struct {
	commands  []string
	rules     map[string]int
	live      map[string]bool
	terminal  map[string]bool
	throws    map[string]map[string]bool
	mutations int
	failAt    int
}

func newReviewRouteKernel() *reviewRouteKernel {
	return &reviewRouteKernel{rules: map[string]int{}, live: map[string]bool{}, terminal: map[string]bool{}, throws: map[string]map[string]bool{}}
}

func reviewArg(args []string, key string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key {
			return args[i+1]
		}
	}
	return ""
}

func (k *reviewRouteKernel) run(args ...string) ([]byte, error) {
	k.commands = append(k.commands, strings.Join(args, " "))
	if len(args) > 0 && args[0] == "-j" {
		return []byte("[]"), nil
	}
	k.mutations++
	if k.failAt > 0 && k.mutations == k.failAt {
		return []byte("injected kernel failure"), errors.New("interrupted apply")
	}
	if len(args) < 4 {
		return nil, errors.New("unexpected command")
	}
	table := reviewArg(args, "table")
	if args[1] == "rule" {
		key := strings.Join(args[3:], " ")
		if args[2] == "add" {
			k.rules[key]++
		} else if args[2] == "del" {
			if k.rules[key] == 0 {
				return []byte("RTNETLINK answers: No such file or directory"), errors.New("missing")
			}
			k.rules[key]--
		}
		return nil, nil
	}
	if args[1] != "route" || table == "" || reviewArg(args, "proto") != l2tpRouteProtocol {
		return nil, errors.New("unexpected route scope")
	}
	switch args[2] {
	case "replace":
		switch args[3] {
		case "unreachable":
			k.terminal[table] = true
		case "default":
			k.live[table] = true
		case "throw":
			if k.throws[table] == nil {
				k.throws[table] = map[string]bool{}
			}
			k.throws[table][args[4]] = true
		}
	case "del":
		if !k.live[table] {
			return []byte("RTNETLINK answers: No such process"), errors.New("missing")
		}
		k.live[table] = false
	case "flush":
		delete(k.live, table)
		delete(k.terminal, table)
		delete(k.throws, table)
	}
	return nil, nil
}

func reviewOnlinePlan() []l2tpRoute {
	return []l2tpRoute{{ID: "review-vpn", Device: l2tp.DeviceName("review-vpn"), Up: true, Sources: []string{"192.0.2.10/32", "192.0.2.11/32"}}}
}

func assertReviewKernelPlan(t *testing.T, k *reviewRouteKernel, plan []l2tpRoute) {
	t.Helper()
	wantRules := map[string]int{}
	for _, route := range plan {
		table := l2tpTable(route.ID)
		if !k.terminal[table] || k.live[table] != route.Up || len(k.throws[table]) != 4 {
			t.Fatalf("incomplete route table: terminal=%v live=%v private=%v plan=%+v", k.terminal, k.live, k.throws, plan)
		}
		for _, source := range route.Sources {
			wantRules["pref "+l2tpRulePriority+" from "+source+" table "+table+" protocol "+l2tpRouteProtocol]++
		}
		if route.Up {
			wantRules["pref "+l2tpRulePriority+" oif "+route.Device+" table "+table+" protocol "+l2tpRouteProtocol]++
		}
	}
	actual := map[string]int{}
	for key, count := range k.rules {
		if count != 0 {
			actual[key] = count
		}
	}
	if !reflect.DeepEqual(actual, wantRules) {
		t.Fatalf("missing/duplicate/stale rules: got=%v want=%v", actual, wantRules)
	}
	if len(plan) == 0 && (len(k.live) != 0 || len(k.terminal) != 0 || len(k.throws) != 0) {
		t.Fatalf("cleanup left tables: live=%v terminal=%v private=%v", k.live, k.terminal, k.throws)
	}
}

func TestL2TPReviewRecoversAfterEveryInitialMutation(t *testing.T) {
	plan := reviewOnlinePlan()
	baseline := newReviewRouteKernel()
	r := &l2tpRoutes{path: filepath.Join(t.TempDir(), "journal.json"), run: baseline.run}
	if err := r.sync(plan); err != nil {
		t.Fatal(err)
	}
	for failure := 1; failure <= baseline.mutations; failure++ {
		t.Run(baseline.commands[failure], func(t *testing.T) {
			k := newReviewRouteKernel()
			k.failAt = failure
			path := filepath.Join(t.TempDir(), "journal.json")
			r := &l2tpRoutes{path: path, run: k.run}
			if err := r.sync(plan); err == nil || !r.dirty {
				t.Fatalf("failure not recorded for retry: err=%v dirty=%v", err, r.dirty)
			}
			k.failAt = 0
			// Alternate same-process retries and recovery in a new process.
			if failure%2 == 0 {
				r = &l2tpRoutes{path: path, run: k.run}
			}
			if err := r.sync(plan); err != nil {
				t.Fatal(err)
			}
			if r.dirty {
				t.Fatal("successful retry remained dirty")
			}
			assertReviewKernelPlan(t, k, plan)
		})
	}
}

func TestL2TPReviewDisconnectRemovesOnlyLiveDefaultAndOIF(t *testing.T) {
	k := newReviewRouteKernel()
	r := &l2tpRoutes{path: filepath.Join(t.TempDir(), "journal.json"), run: k.run}
	plan := reviewOnlinePlan()
	if err := r.sync(plan); err != nil {
		t.Fatal(err)
	}
	k.commands = nil
	plan = reviewOnlinePlan()
	plan[0].Up = false
	if err := r.sync(plan); err != nil {
		t.Fatal(err)
	}
	assertReviewKernelPlan(t, k, plan)
	joined := strings.Join(k.commands, "\n")
	if strings.Index(joined, "route replace unreachable default") > strings.Index(joined, "route del default") {
		t.Fatal("live default removed before fallback blocking route was ensured")
	}
	if !strings.Contains(joined, "rule del pref 8000 oif ") || strings.Contains(joined, "rule add pref 8000 oif ") {
		t.Fatal("disconnect did not remove the outgoing-interface rule")
	}
	plan = reviewOnlinePlan()
	if err := r.sync(plan); err != nil {
		t.Fatal(err)
	}
	assertReviewKernelPlan(t, k, plan)
}

func TestL2TPReviewCleanupAfterAddressChangeUsesJournalUnion(t *testing.T) {
	k := newReviewRouteKernel()
	path := filepath.Join(t.TempDir(), "journal.json")
	r := &l2tpRoutes{path: path, run: k.run}
	old := reviewOnlinePlan()
	if err := r.sync(old); err != nil {
		t.Fatal(err)
	}
	next := reviewOnlinePlan()
	next[0].Sources = []string{"192.0.2.12/32"}
	// Fail before replacing any routes. Cleanup after the process dies must
	// account for both the previously applied and newly desired source IPs.
	k.failAt = k.mutations + 1
	if err := r.sync(next); err == nil {
		t.Fatal("injected update did not fail")
	}
	var journal []l2tpRoute
	b, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(b, &journal) != nil || len(journal) != 1 || len(journal[0].Sources) != 3 || !journal[0].Up {
		t.Fatalf("journal lost prior bindings: %s err=%v", b, err)
	}
	k.failAt, k.commands = 0, nil
	recovered := &l2tpRoutes{path: path, run: k.run}
	if err := recovered.sync(nil); err != nil {
		t.Fatal(err)
	}
	assertReviewKernelPlan(t, k, nil)
	joined := strings.Join(k.commands, "\n")
	for _, source := range []string{"192.0.2.10/32", "192.0.2.11/32", "192.0.2.12/32"} {
		if !strings.Contains(joined, "rule del pref 8000 from "+source) {
			t.Fatalf("cleanup ignored journal source %s", source)
		}
	}
}

func TestL2TPReviewCleanupFailureRemainsRetryable(t *testing.T) {
	k := newReviewRouteKernel()
	r := &l2tpRoutes{path: filepath.Join(t.TempDir(), "journal.json"), run: k.run}
	if err := r.sync(reviewOnlinePlan()); err != nil {
		t.Fatal(err)
	}
	k.failAt = k.mutations + 2 // The first source rule is already gone.
	if err := r.sync(nil); err == nil || !r.dirty {
		t.Fatalf("partial cleanup not retained: err=%v dirty=%v", err, r.dirty)
	}
	k.failAt = 0
	if err := r.sync(nil); err != nil {
		t.Fatal(err)
	}
	assertReviewKernelPlan(t, k, nil)
}

func TestL2TPReviewUnchangedRefreshFailureMarksRoutesUnready(t *testing.T) {
	k := newReviewRouteKernel()
	r := &l2tpRoutes{path: filepath.Join(t.TempDir(), "journal.json"), run: k.run}
	plan := reviewOnlinePlan()
	if err := r.sync(plan); err != nil {
		t.Fatal(err)
	}
	k.failAt = k.mutations + 1
	if err := r.sync(plan); err == nil {
		t.Fatal("refresh unexpectedly succeeded")
	}
	if !r.dirty {
		t.Fatal("failed route refresh is still reported ready")
	}
	k.failAt = 0
	if err := r.sync(plan); err != nil {
		t.Fatal(err)
	}
	assertReviewKernelPlan(t, k, plan)
}
