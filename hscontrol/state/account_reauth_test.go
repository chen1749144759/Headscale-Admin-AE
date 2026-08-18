package state

import (
	"net/netip"
	"testing"
	"time"

	"github.com/juanfont/headscale/hscontrol/types"
	"github.com/juanfont/headscale/hscontrol/util"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

func TestPasswordReauthMigratesLegacyAuthKeyNode(t *testing.T) {
	config := newAccountStartupTestConfig(t)
	first, err := NewState(config)
	if err != nil {
		t.Fatalf("creating initial state: %v", err)
	}

	user, err := first.db.CreateUser(types.User{Name: "legacy-network"})
	if err != nil {
		t.Fatalf("creating user: %v", err)
	}
	machineKey := key.NewMachine().Public()
	oldNodeKey := key.NewNode().Public()
	node := types.Node{
		MachineKey:     machineKey,
		NodeKey:        oldNodeKey,
		DiscoKey:       key.NewDisco().Public(),
		Hostname:       "legacy-router",
		GivenName:      "legacy-router",
		UserID:         &user.ID,
		RegisterMethod: util.RegisterMethodAuthKey,
		Hostinfo:       &tailcfg.Hostinfo{Hostname: "legacy-router"},
	}
	if err := first.db.DB.Create(&node).Error; err != nil {
		t.Fatalf("creating legacy node: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("closing initial state: %v", err)
	}

	second, err := NewState(config)
	if err != nil {
		t.Fatalf("reopening state: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	newNodeKey := key.NewNode().Public()
	authID := types.MustAuthID()
	second.SetAuthCacheEntry(authID, types.NewRegisterAuthRequest(&types.RegistrationData{
		MachineKey: machineKey,
		NodeKey:    newNodeKey,
		DiscoKey:   key.NewDisco().Public(),
		Hostname:   "legacy-router",
		Hostinfo:   &tailcfg.Hostinfo{Hostname: "legacy-router"},
	}))
	expiry := time.Now().UTC().Add(24 * time.Hour)
	updated, _, err := second.HandleNodeFromAuthPath(
		authID,
		types.UserID(user.ID),
		&expiry,
		util.RegisterMethodPassword,
	)
	if err != nil {
		t.Fatalf("reauthenticating legacy node: %v", err)
	}
	if got := updated.RegisterMethod(); got != util.RegisterMethodPassword {
		t.Fatalf("updated register method = %q, want %q", got, util.RegisterMethodPassword)
	}
	if got := updated.NodeKey(); got != newNodeKey {
		t.Fatalf("updated node key = %q, want %q", got, newNodeKey)
	}

	var persisted types.Node
	if err := second.db.DB.First(&persisted, node.ID).Error; err != nil {
		t.Fatalf("loading persisted node: %v", err)
	}
	if got := persisted.RegisterMethod; got != util.RegisterMethodPassword {
		t.Fatalf("persisted register method = %q, want %q", got, util.RegisterMethodPassword)
	}
}

func TestPasswordRegistrationTakesOverExistingAccountNode(t *testing.T) {
	config := newAccountStartupTestConfig(t)
	first, err := NewState(config)
	if err != nil {
		t.Fatalf("creating initial state: %v", err)
	}

	user, err := first.db.CreateUser(types.User{Name: "single-login-network"})
	if err != nil {
		t.Fatalf("creating user: %v", err)
	}
	oldMachineKey := key.NewMachine().Public()
	oldNodeKey := key.NewNode().Public()
	ipv4 := netip.MustParseAddr("100.64.0.42")
	approvedRoute := netip.MustParsePrefix("192.0.2.0/24")
	oldNode := types.Node{
		MachineKey:     oldMachineKey,
		NodeKey:        oldNodeKey,
		DiscoKey:       key.NewDisco().Public(),
		Hostname:       "old-client",
		GivenName:      "account-node",
		UserID:         &user.ID,
		RegisterMethod: util.RegisterMethodPassword,
		Hostinfo:       &tailcfg.Hostinfo{Hostname: "old-client"},
		IPv4:           &ipv4,
		ApprovedRoutes: []netip.Prefix{approvedRoute},
	}
	if err := first.db.DB.Create(&oldNode).Error; err != nil {
		t.Fatalf("creating existing account node: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("closing initial state: %v", err)
	}

	second, err := NewState(config)
	if err != nil {
		t.Fatalf("reopening state: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	newMachineKey := key.NewMachine().Public()
	newNodeKey := key.NewNode().Public()
	authID := types.MustAuthID()
	second.SetAuthCacheEntry(authID, types.NewRegisterAuthRequest(&types.RegistrationData{
		MachineKey: newMachineKey,
		NodeKey:    newNodeKey,
		DiscoKey:   key.NewDisco().Public(),
		Hostname:   "replacement-client",
		Hostinfo:   &tailcfg.Hostinfo{Hostname: "replacement-client"},
	}))
	expiry := time.Now().UTC().Add(24 * time.Hour)
	updated, _, err := second.HandleNodeFromAuthPath(
		authID,
		types.UserID(user.ID),
		&expiry,
		util.RegisterMethodPassword,
	)
	if err != nil {
		t.Fatalf("taking over existing account node: %v", err)
	}
	if updated.ID() != oldNode.ID {
		t.Fatalf("updated node ID = %d, want preserved ID %d", updated.ID(), oldNode.ID)
	}
	if updated.MachineKey() != newMachineKey || updated.NodeKey() != newNodeKey {
		t.Fatalf("updated keys = %q, %q; want %q, %q", updated.MachineKey(), updated.NodeKey(), newMachineKey, newNodeKey)
	}
	if got := updated.IPv4(); !got.Valid() || got.Get() != ipv4 {
		t.Fatalf("updated IPv4 = %v; want %v", got, ipv4)
	}
	if got := updated.ApprovedRoutes(); got.Len() != 1 || got.At(0) != approvedRoute {
		t.Fatalf("updated approved routes = %v, want [%v]", got, approvedRoute)
	}
	if got := second.ListNodesByUser(types.UserID(user.ID)).Len(); got != 1 {
		t.Fatalf("account node count = %d, want 1", got)
	}
	if _, ok := second.GetNodeByMachineKey(oldMachineKey, types.UserID(user.ID)); ok {
		t.Fatal("old machine key still resolves after single sign-on takeover")
	}
	if _, ok := second.GetNodeByNodeKey(oldNodeKey); ok {
		t.Fatal("old node key still resolves after single sign-on takeover")
	}
	if got, ok := second.GetNodeByMachineKey(newMachineKey, types.UserID(user.ID)); !ok || got.ID() != oldNode.ID {
		t.Fatalf("new machine key lookup = %v, %v; want node %d", got, ok, oldNode.ID)
	}

	var persisted types.Node
	if err := second.db.DB.First(&persisted, oldNode.ID).Error; err != nil {
		t.Fatalf("loading replaced node: %v", err)
	}
	if persisted.MachineKey != newMachineKey || persisted.NodeKey != newNodeKey {
		t.Fatalf("persisted keys = %q, %q; want %q, %q", persisted.MachineKey, persisted.NodeKey, newMachineKey, newNodeKey)
	}
}
