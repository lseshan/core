// Copyright (c) 2016 Pani Networks
// All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"); you may
// not use this file except in compliance with the License. You may obtain
// a copy of the License at
//
//   http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
// WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
// License for the specific language governing permissions and limitations
// under the License.
//
// IPtables based firewall implementations.

package firewall

import (
	"fmt"
	"github.com/golang/glog"
	utilexec "github.com/romana/core/pkg/util/exec"
	"net"
	"strconv"
)

const (
	InputChainIndex      = 0
	OutputChainIndex     = 1
	ForwardInChainIndex  = 2
	ForwardOutChainIndex = 3

	targetDrop   = "DROP"
	targetAccept = "ACCEPT"
)

// IPtables implements romana Firewall using iptables.
type IPtables struct {
	Chains        []IPtablesChain
	U32Filter     string
	ChainPrefix   string
	InterfaceName string
	Environment   FirewallEnvironment
	Store         firewallStore
	os            utilexec.Executable

	// Discovered run-time configuration.
	networkConfig NetConfig
}

// Implement firewall Provider method.
func (fw IPtables) Provider() string {
	return "iptables"
}

// Implements firewall SetDefaultRules method.
func (fw IPtables) SetDefaultRules(rules []*IPtablesRule, chainIndex int) {
	for _, rule := range rules {
		rule.Body = fmt.Sprintf("%s %s", fw.Chains[chainIndex].ChainName, rule.Body)
	}
	fw.Chains[chainIndex].Rules = rules
}

// IPtablesChain describes state of the particular firewall chain.
type IPtablesChain struct {
	BaseChain  string
	Directions []string
	Rules      []*IPtablesRule
	ChainName  string
}

// NewIPtablesChain initializes a new firewall chain.
func NewIPtablesChain(baseChain string, direction []string, rules []*IPtablesRule, chainName string) *IPtablesChain {
	return &IPtablesChain{baseChain, direction, rules, chainName}
}

/*
// Collection of firewall Rules.
type firewallRules []string

// Add appends a new firewall rule to the collection of firewall Rules.
func (r *firewallRules) Add(content string) {
	*r = append(*r, content)
}
*/

// prepareChainName returns a chain name with tenant-segment specific prefix.
func (fw *IPtables) prepareChainName(chainName string) string {
	return fmt.Sprintf("%s%s", fw.ChainPrefix, chainName)
}

// makeRules generates Rules for given endpoint on given environment
func (fw *IPtables) makeRules(netif FirewallEndpoint) error {
	var err error
	fw.U32Filter, fw.ChainPrefix, err = fw.prepareU32Rules(netif.GetIP())
	if err != nil {
		// TODO need personalized error here, or even panic
		return err
	}
	fw.InterfaceName = netif.GetName()

	/*
		// Allow ICMP, DHCP and SSH between host and instances.
		// hostAddr := fw.networkConfig.RomanaGW()
		inputRules := []string{
		//	"-d 255.255.255.255/32 -p udp -m udp --sport 68 --dport 67",
		}

		outputRules := []string{
		//	fmt.Sprintf("-s %s/32 -p udp -m udp --sport 67 --dport 68", hostAddr),
		}

		forwardRules := []string{
		//	"-m comment --comment Outgoing",
		}

		tenantVectorRules := []string{
		//	"-m state --state RELATED,ESTABLISHED",
		}
	*/

	tenantVectorChainName := fmt.Sprintf("ROMANA-T%d", fw.extractTenantID(ipToInt(netif.GetIP())))

	fw.Chains = append(fw.Chains, IPtablesChain{
		BaseChain:  "INPUT",
		Directions: []string{"i"},
		ChainName:  fw.prepareChainName("INPUT"),
	})
	fw.Chains = append(fw.Chains, IPtablesChain{
		BaseChain:  "OUTPUT",
		Directions: []string{"o"},
		ChainName:  fw.prepareChainName("OUTPUT"),
	})
	fw.Chains = append(fw.Chains, IPtablesChain{
		BaseChain:  "FORWARD",
		Directions: []string{"i"},
		ChainName:  fw.prepareChainName("FORWARD"),
	})
	fw.Chains = append(fw.Chains, IPtablesChain{
		BaseChain:  "FORWARD",
		Directions: []string{"o"},
		ChainName:  tenantVectorChainName,
	})

	return nil
}

// isChainExist verifies if given iptables chain exists.
// Returns true if chain exists.
func (fw *IPtables) isChainExist(chain int) bool {
	cmd := "/sbin/iptables"
	args := []string{"-L", fw.Chains[chain].ChainName}
	output, err := fw.os.Exec(cmd, args)
	if err != nil {
		return false
	}
	glog.Infof("isChainExist(): iptables -L %s returned %s", fw.Chains[chain].ChainName, string(output))
	return true
}

// isRuleExist verifies if given iptables rule exists.
// Returns true rule exists.
func (fw *IPtables) isRuleExist(rule *IPtablesRule) bool {
	cmd := "/sbin/iptables"
	args := []string{"-C", rule.Body}
	_, err := fw.os.Exec(cmd, args)
	if err != nil {
		return false
	}
	return true
}

// detectMissingChains checks which IPtables Chains haven't been created yet.
// Because we do not want to create Chains that already exist.
func (fw *IPtables) detectMissingChains() []int {
	var ret []int
	for chain := range fw.Chains {
		glog.Infof("Testing chain", chain)
		if !fw.isChainExist(chain) {
			glog.Infof(">> Testing chain success", chain)
			ret = append(ret, chain)
		}
	}
	return ret
}

// CreateChains creates IPtables Chains such as
// ROMANA-T0S0-OUTPUT, ROMANA-T0S0-FORWARD, ROMANA-T0S0-INPUT.
func (fw *IPtables) CreateChains(newChains []int) error {
	for chain := range newChains {
		cmd := "/sbin/iptables"
		args := []string{"-N", fw.Chains[chain].ChainName}
		_, err := fw.os.Exec(cmd, args)
		if err != nil {
			return err
		}
	}
	return nil
}

// opDivertTrafficAction is a parameter for DivertTrafficToRomanaIPtablesChain
// functions that indicates action to be taken.
type opDivertTrafficAction int

const (
	installDivertRules opDivertTrafficAction = iota
	removeDivertRules
)

func (d opDivertTrafficAction) String() string {
	var result string
	switch d {
	case installDivertRules:
		result = "Installing divert rules"
	case removeDivertRules:
		result = "Removing divert rules"
	}
	return result
}

// DivertTrafficToRomanaIPtablesChain injects iptables Rules to send traffic
// into the ROMANA chain.
// We need to do this for each tenant/segment pair as each pair will have different chain name.
func (fw *IPtables) DivertTrafficToRomanaIPtablesChain(chain IPtablesChain, opType opDivertTrafficAction) error {
	// Should be like that
	// iptables -A INPUT -i tap1234 -j ROMANA-T0S1-INPUT
	glog.Infof("In DivertTrafficToRomanaIPtablesChain() processing chain %v with state %s", chain, opType)

	var state RuleState
	switch opType {
	case installDivertRules:
		state = ensureLast
	case removeDivertRules:
		state = ensureAbsent
	}

	// baseChain := fw.Chains[chain].BaseChain
	for _, directionLiteral := range chain.Directions {
		direction := fmt.Sprintf("-%s", directionLiteral)
		body := fmt.Sprintf("%s %s %s %s %s", chain.BaseChain, direction, fw.InterfaceName, "-j", chain.ChainName)
		rule := &IPtablesRule{
			Body:  body,
			State: setRuleInactive.String(),
		}
		// ruleSpec := []string{baseChain, direction, fw.InterfaceName, "-j", chainName}

		// First create rule record in database.
		err0 := fw.addIPtablesRule(rule)
		if err0 != nil {
			glog.Errorf("In DivertTrafficToRomanaIPtablesChain() failed to process chain %v", chain)
			return err0
		}

		// Then create actuall rule in the system.
		if err1 := fw.EnsureRule(rule, state); err1 != nil {
			glog.Errorf("In DivertTrafficToRomanaIPtablesChain() failed to process chain %v", chain)
			return err1
		}

		// Finally, set 'active' flag in database record.
		if err2 := fw.Store.switchIPtablesRule(rule, setRuleActive); err2 != nil {
			glog.Error("In DivertTrafficToRomanaIPtablesChain() iptables rule created but activation failed ", rule.Body)
			return err2
		}

	}
	glog.Info("DivertTrafficToRomanaIPtablesChain() successfully processed chain number", chain)
	return nil
}

// addIPtablesRule creates new iptable rule in database.
func (fw *IPtables) addIPtablesRule(rule *IPtablesRule) error {
	//	rule := new(IPtablesRule)
	//	rule.Body = strings.Join(ruleSpec, " ")
	//	rule.State = setRuleInactive.String()

	if err := fw.Store.addIPtablesRule(rule); err != nil {
		glog.Error("In addIPtablesRule failed to add ", rule.Body)
		return err
	}

	return nil
}

// CreateRules creates iptables Rules for the given Romana chain
// to allow a traffic to flow between the Host and Endpoint.
func (fw *IPtables) CreateRules(chain int) error {
	glog.Info("In CreateRules() for chain", chain)
	for _, rule := range fw.Chains[chain].Rules {
		//		chainName := fw.Chains[chain].ChainName
		//		ruleSpec := []string{chainName}
		//		ruleSpec = append(ruleSpec, strings.Split(fw.Chains[chain].Rules[rule], " ")...)
		//		ruleSpec = append(ruleSpec, []string{"-j", "ACCEPT"}...)

		// First create rule record in database.
		err0 := fw.addIPtablesRule(rule)
		if err0 != nil {
			glog.Error("In CreateRules() create db record for iptables rule ", rule.Body)
			return err0
		}

		err1 := fw.EnsureRule(rule, ensureFirst)
		if err1 != nil {
			glog.Error("In CreateRules() failed to create install firewall rule ", rule.Body)
			return err1
		}

		// Finally, set 'active' flag in database record.
		if err2 := fw.Store.switchIPtablesRule(rule, setRuleActive); err2 != nil {
			glog.Error("In CreateRules() iptables rule created but activation failed ", rule.Body)
			return err2
		}
	}
	glog.Info("Creating firewall rules success")
	return nil
}

// CreateU32Rules creates wildcard iptables Rules for the given Romana chain.
// These Rules serve to restrict traffic between segments and tenants.
// * Deprecated, outdated *
func (fw *IPtables) CreateU32Rules(chain int) error {
	glog.Info("Creating U32 firewall rules for chain", chain)
	chainName := fw.Chains[chain].ChainName
	cmd := "/sbin/iptables"
	args := []string{"-A", chainName, "-m", "u32", "--u32", fw.U32Filter, "-j", "ACCEPT"}
	_, err := fw.os.Exec(cmd, args)
	if err != nil {
		glog.Error("Creating U32 firewall rules failed")
		return err
	}
	glog.Info("Creating U32 firewall rules success")
	return nil
}

// CreateDefaultDropRule creates iptables Rules to drop all unidentified traffic
// in the given chain
func (fw *IPtables) CreateDefaultDropRule(chain int) error {
	return fw.CreateDefaultRule(chain, targetDrop)
}

// CreateDefaultRule creates iptables rule for a chain with the
// specified target
func (fw *IPtables) CreateDefaultRule(chain int, target string) error {
	glog.Infof("In CreateDefaultRule() %s rules for chain %d", target, chain)
	chainName := fw.Chains[chain].ChainName
	//	ruleSpec := []string{chainName, "-j", target}
	body := fmt.Sprintf("%s %s %s", chainName, "-j", target)
	rule := &IPtablesRule{
		Body:  body,
		State: setRuleActive.String(),
	}

	// First create rule record in database.
	err0 := fw.addIPtablesRule(rule)
	if err0 != nil {
		glog.Error("In CreateDefaultRules() create db record for iptables rule ", rule.Body)
		return err0
	}

	err1 := fw.EnsureRule(rule, ensureLast)
	if err1 != nil {
		glog.Errorf("In CreateDefaultRules() %s rules failed", target)
		return err1
	}

	// Finally, set 'active' flag in database record.
	if err2 := fw.Store.switchIPtablesRule(rule, setRuleActive); err2 != nil {
		glog.Error("In CreateDefaultRules() iptables rule created but activation failed ", rule.Body)
		return err2
	}

	glog.Info("In CreateDefaultRules() success")
	return nil
}

// prepareTenantSegmentMask returns integer representation of a bitmask
// for tenant+segment bits in pseudo network.
func (fw *IPtables) prepareTenantSegmentMask() uint64 {
	var res uint64
	tenantBits := fw.networkConfig.TenantBits()
	segmentBits := fw.networkConfig.SegmentBits()
	combinedTSBits := tenantBits + segmentBits
	endpointBits := fw.networkConfig.EndpointBits()
	res = ((1 << combinedTSBits) - 1) << endpointBits
	return res
}

// ipToInt transforms IP address from net.IP form to integer form.
// Taken from IPAM/config, should really be in some shared library.
func ipToInt(ip net.IP) uint64 {
	return uint64(ip[12])<<24 | uint64(ip[13])<<16 | uint64(ip[14])<<8 | uint64(ip[15])
}

// PseudoNetNetmaskInt returns integer representation of pseudo net netmask.
func (fw *IPtables) PseudoNetNetmaskInt() (uint64, error) {
	cidr, err := fw.networkConfig.PNetCIDR()
	if err != nil {
		return 0, err
	}
	pNetMaskInt, err := MaskToInt(cidr.Mask)
	if err != nil {
		return 0, err
	}
	return pNetMaskInt, nil
}

// MaskToInt converts net.IPMask to integer.
// TODO Not strictly firewall method, maybe put in different place.
func MaskToInt(mask net.IPMask) (uint64, error) {
	var imask uint64
	m, err := strconv.ParseInt(mask.String(), 16, 64)
	imask = uint64(m)
	if err != nil {
		return 0, err
	}
	return imask, nil
}

// prepareU32Rules generates IPtables Rules for U32 iptables module.
// This Rules implemet Romana tenant/segment filtering
//   Return the filter Rules for the iptables u32 module.
//   Goal: Filter out any traffic that does not have the same tenant and segment
//   bits in the destination address as the interface itself.
//   These bits can be extracted from the IP address: This is the address that
//   we are assigning to the interface. The function is to be called when the
//   interface is set up. The passed-in address therefore can be trusted: It is
//   not taken from a packet.
//      Example:
//      ipAddr = "10.0.1.4"
//
//      Return:
//      filter = '12&0xFF00FF00=0xA000100 && 16&0xFF00FF00=0xA000100'
//      ChainPrefix = 'ROMANA-T0S1-'
//
//   TODO Refactor chain-prefix routine into separate function (prepareChainPrefix).
//   Also return the chain-prefix we'll use for this interface. This is
//   typically a string such as:
//       ROMANA-T<tenant-id>S<segment-id>-
//   For example, with tenant 1 and segment 2, this would be:
//       ROMANA-T1S2-
func (fw *IPtables) prepareU32Rules(ipAddr net.IP) (string, string, error) {
	fullMask, err := fw.prepareNetmaskBits()
	if err != nil {
		return "", "", err
	}
	addr := ipToInt(ipAddr)
	if err != nil {
		return "", "", err
	}
	filter1 := fmt.Sprintf("0x%X=0x%X", fullMask, addr&fullMask)
	filter := fmt.Sprintf("12&%s && 16&%s", filter1, filter1)
	tenantID := fw.extractTenantID(addr)
	segmentID := fw.extractSegmentID(addr)
	chainPrefix := fmt.Sprintf("ROMANA-T%dS%d-", tenantID, segmentID)
	return filter, chainPrefix, nil
}

// prepareNetmaskBits returns integer representation of pseudo network bitmask.
// Used to prepare u32 firewall Rules that would match ip addresses belonging
// to given tenant/segment pair.
func (fw *IPtables) prepareNetmaskBits() (uint64, error) {
	iCidrMask, err := fw.PseudoNetNetmaskInt()
	if err != nil {
		return 0, err
	}
	combinedTSMask := fw.prepareTenantSegmentMask()
	res := iCidrMask | combinedTSMask
	return res, nil
}

// extractSegmentID extracts segment id from the given ip address.
// This is possible because segment id encoded in the ip address.
func (fw *IPtables) extractSegmentID(addr uint64) uint64 {
	endpointBits := fw.networkConfig.EndpointBits()
	segmentBits := fw.networkConfig.SegmentBits()
	sid := (addr >> endpointBits) & ((1 << segmentBits) - 1)
	return sid
}

// extractTenantID extracts tenant id from given the ip address.
// This is possible because tenant id encoded in the ip address.
func (fw *IPtables) extractTenantID(addr uint64) uint64 {
	endpointBits := fw.networkConfig.EndpointBits()
	segmentBits := fw.networkConfig.SegmentBits()
	tenantBits := fw.networkConfig.TenantBits()
	tid := (addr >> (endpointBits + segmentBits)) & ((1 << tenantBits) - 1)
	return tid
}

// ProvisionEndpoint creates iptables Rules for given endpoint in given environment
func (fw IPtables) ProvisionEndpoint(netif FirewallEndpoint) error {
	glog.Info("In ProvisionEndpoint()")
	var err error

	if err = fw.makeRules(netif); err != nil {
		return err
	}

	switch fw.Environment {
	case KubernetesEnvironment:
		err = fw.provisionK8SIPtablesRules()
	case OpenStackEnvironment:
		err = fw.provisionIPtablesRules()
	}

	return err
}

// Cleanup implements Firewall interface.
func (fw IPtables) Cleanup(netif FirewallEndpoint) error {
	if err := fw.deleteIPtablesRulesBySubstring(netif.GetName()); err != nil {
		glog.Errorf("In Cleanup() failed to clean firewall for %s", netif.GetName())
		return err
	}

	return nil
}

// deleteIPtablesRulesBySubstring uninstalls iptables Rules matching given
// substring and deletes them from database. Has no effect on 'inactive' Rules.
func (fw *IPtables) deleteIPtablesRulesBySubstring(substring string) error {
	rules, err := fw.Store.findIPtablesRules(substring)
	if err != nil {
		return err
	}

	for _, rule := range *rules {
		if rule.State == setRuleInactive.String() {
			continue
		}

		err = fw.deleteIPtablesRule(&rule)
		if err != nil {
			return err
		}
	}

	return nil
}

// deleteIPtablesRule attempts to uninstall and delete the given rule.
func (fw *IPtables) deleteIPtablesRule(rule *IPtablesRule) error {
	if err := fw.Store.switchIPtablesRule(rule, setRuleInactive); err != nil {
		glog.Error("In deleteIPtablesRule() failed to deactivate the rule", rule.Body)
		return err
	}

	if err1 := fw.EnsureRule(rule, ensureAbsent); err1 != nil {
		glog.Errorf("In deleteIPtablesRule() rule %s set inactive but failed to uninstall", rule.Body)
		return err1
	}

	if err2 := fw.Store.deleteIPtablesRule(rule); err2 != nil {
		glog.Errorf("In deleteIPtablesRule() rule %s set inactive and uninstalled but failed to delete DB record", rule.Body)
		return err2
	}
	return nil
}

// EnsureRule verifies if given iptables rule exists and creates if it's not.
func (fw IPtables) EnsureRule(rule *IPtablesRule, opType RuleState) error {
	ruleExists := fw.isRuleExist(rule)
	cmd := "/sbin/iptables"
	args := []string{}

	if ruleExists && opType == ensureAbsent {
		args = append(args, []string{"-D"}...)

	} else if !ruleExists {

		switch opType {
		case ensureLast:
			args = append(args, []string{"-A"}...)
		case ensureFirst:
			args = append(args, []string{"-I"}...)
		}
	} else {
		glog.Infof("In EnsureRule - nothing to do ", rule.Body)
		return nil
	}

	args = append(args, rule.Body)
	_, err := fw.os.Exec(cmd, args)
	if err != nil {
		glog.Errorf("%s filed %s", opType, rule.Body)
	} else {
		glog.Infof("%s success %s", opType, rule.Body)
	}

	return err
}

// ListRules implements Firewall interface
func (fw IPtables) ListRules() ([]IPtablesRule, error) {
	return fw.Store.listIPtablesRules()
}
