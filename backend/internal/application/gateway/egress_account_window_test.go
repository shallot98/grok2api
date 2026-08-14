package gateway

import (
	"testing"
	"time"

	"github.com/chenyme/grok2api/backend/internal/domain/account"
)

func TestEgressAccountWindowLimitsDistinctBuildAccounts(t *testing.T) {
	var window egressAccountWindow
	window.update(10*time.Minute, 2)
	now := time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)
	credential := func(id, nodeID uint64) account.Credential {
		return account.Credential{ID: id, Provider: account.ProviderBuild, EgressNodeID: nodeID}
	}

	if !window.record(credential(1, 10), now) || !window.record(credential(2, 10), now.Add(time.Minute)) {
		t.Fatal("first two distinct accounts should fit")
	}
	if allowed, retry := window.allows(credential(3, 10), now.Add(2*time.Minute)); allowed || retry != 8*time.Minute {
		t.Fatalf("third account allowed=%v retry=%s", allowed, retry)
	}
	if !window.record(credential(1, 10), now.Add(3*time.Minute)) {
		t.Fatal("an account already in the window should remain allowed")
	}
	if !window.record(credential(3, 11), now.Add(3*time.Minute)) {
		t.Fatal("a different egress node should have independent capacity")
	}
	if !window.record(credential(3, 10), now.Add(12*time.Minute)) {
		t.Fatal("expired accounts should release capacity")
	}
}

func TestEgressAccountWindowBypassesOtherProvidersAndUnboundBuild(t *testing.T) {
	var window egressAccountWindow
	window.update(10*time.Minute, 1)
	now := time.Now().UTC()

	if !window.record(account.Credential{ID: 1, Provider: account.ProviderWeb, EgressNodeID: 10}, now) {
		t.Fatal("web accounts should bypass the Build window")
	}
	if !window.record(account.Credential{ID: 2, Provider: account.ProviderBuild}, now) {
		t.Fatal("unbound Build accounts should bypass the node window")
	}
}
