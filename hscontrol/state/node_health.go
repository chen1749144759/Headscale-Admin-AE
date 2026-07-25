package state

import (
	"fmt"

	"github.com/juanfont/headscale/hscontrol/types"
	"github.com/juanfont/headscale/hscontrol/util/zlog/zf"
	"github.com/rs/zerolog/log"
)

type nodeHealthCheck struct {
	name  string
	check func(types.NodeView, *types.Config) (problem, fix string, ok bool)
}

var nodeHealthChecks = []nodeHealthCheck{
	{
		name: "given-name-maps-to-valid-fqdn",
		check: func(node types.NodeView, cfg *types.Config) (string, string, bool) {
			if err := types.ValidateGivenName(node.GivenName(), cfg.BaseDomain); err != nil {
				return err.Error(), fmt.Sprintf("headscale nodes rename %d <name>", node.ID()), false
			}

			return "", "", true
		},
	},
}

type nodeHealthFinding struct {
	nodeID   types.NodeID
	hostname string
	check    string
	problem  string
	fix      string
}

func (s *State) scanNodeHealth() []nodeHealthFinding {
	var findings []nodeHealthFinding

	for _, node := range s.nodeStore.ListNodes().All() {
		for _, check := range nodeHealthChecks {
			problem, fix, ok := check.check(node, s.cfg)
			if !ok {
				findings = append(findings, nodeHealthFinding{
					nodeID: node.ID(), hostname: node.Hostname(), check: check.name,
					problem: problem, fix: fix,
				})
			}
		}
	}

	return findings
}

func (s *State) logNodeHealth() {
	for _, finding := range s.scanNodeHealth() {
		log.Warn().
			Uint64(zf.NodeID, finding.nodeID.Uint64()).
			Str(zf.NodeHostname, finding.hostname).
			Str("check", finding.check).
			Str("problem", finding.problem).
			Str("fix", finding.fix).
			Msg("node has invalid data that breaks map generation")
	}
}
