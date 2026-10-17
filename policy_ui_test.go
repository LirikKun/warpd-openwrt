package main

import (
	"os/exec"
	"strings"
	"testing"
)

// Execute the actual page helpers together, not an isolated function with
// hand-copied policy logic. Full DOM interactions are in ui_interaction.test.cjs.
func runUIRenderAssertions(t *testing.T, assertions string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node needed for offline UI rendering test")
	}
	start := strings.Index(indexHTML, "'use strict';")
	end := strings.Index(indexHTML, "buildRows();buildConfig();")
	if start < 0 || end <= start {
		t.Fatal("embedded page script missing")
	}
	script := `const elements={};
 const document={activeElement:null,getElementById(id){return elements[id]||(elements[id]={textContent:'',className:'',value:'',dataset:{},setAttribute(k,v){this[k]=v;}})}};
 const location={href:'http://router/',hostname:'router'};const history={replaceState(){}};
 const assert=require('node:assert/strict');
 ` + indexHTML[start:end] + `
 connected=true;receivedAt=Date.now();lastStatus={config:{check_period:30,exclude_nodes:'DME',exclude_countries:'RU'},proxies:{}};
 ` + assertions
	if out, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("offline UI render failed: %v\n%s", err, out)
	}
}

func TestPolicyUIShowsUnsuitableRatherThanStarting(t *testing.T) {
	runUIRenderAssertions(t, `
 const p={enabled:true,running:true,transport_ready:true,policy_ready:false,ready:false,port:4005,last_check:new Date().toISOString(),exit_info:'RU via Frankfurt (FRA node)',exit_node:'FRA',exit_node_country:'DE',exit_country:'RU',policy_error:'foreign IP not verified',foreign_ip_verified:false,checks:1,ok_checks:1};
 renderProxy('m2w',p);
 assert.match($('m2w-policy').textContent,/выход не подходит/);
 assert.notEqual($('m2w-policy').className,'ok','unsuitable exit is green');
 assert.ok($('m2w-policy-error').textContent.includes(p.policy_error),'policy_error hidden');
 assert.match($('m2w-geo').textContent,/Иностранный IP не подтверждён/,'M2W gap hidden');
 renderProxy('warp',{...p,ready:true,policy_ready:true,policy_error:'',port:4001});
 assert.equal($('warp-transport').className,'ok');assert.equal($('warp-policy').className,'ok');
 assert.equal($('warp-ip-country').textContent,'RU','standalone IP-country incorrectly suppressed');
 renderProxy('warp',{...p,transport_ready:false,policy_ready:false,ready:false});
 assert.notEqual($('warp-transport').className,'ok','stale transport is green');
 assert.notEqual($('warp-policy').className,'ok','stale exit remains green');
 assert.match($('warp-transport').textContent,/нет свежей успешной проверки/);
 for (const country of ['RU','']) {renderProxy('warp',{...p,ready:true,policy_ready:true,exit_node:'DME',exit_node_country:country});assert.notEqual($('warp-policy').className,'ok');}
 `)
}
