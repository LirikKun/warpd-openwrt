package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestPolicyUIShowsUnsuitableRatherThanStarting(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node needed for offline UI rendering test")
	}
	start := strings.Index(indexHTML, "function renderProxy(")
	end := strings.Index(indexHTML, "function renderScan(")
	if start < 0 || end <= start {
		t.Fatal("renderProxy not found")
	}
	script := `const elements={};
 const $=id=>elements[id]||(elements[id]={textContent:'',className:'',classList:{add(){},remove(){}}});
 const location={hostname:'router'}; const fmtTime=()=> 'now';
 ` + indexHTML[start:end] + `
 const assert=require('assert');
 const p={enabled:true,running:true,transport_ready:true,policy_ready:false,ready:false,port:4005,exit_info:'RU via Frankfurt (FRA node)',exit_node:'FRA',exit_node_country:'DE',exit_country:'RU',policy_error:'foreign IP not verified',foreign_ip_verified:false,checks:1,ok_checks:1};
 renderProxy('m2w',p);
 assert.strictEqual($('m2w-badge').textContent,'выход не подходит');
 assert.ok(!$('m2w-exit').className.split(' ').includes('ok'),'non-DME unsuitable exit is green');
 assert.ok($('m2w-check').textContent.includes(p.policy_error),'policy_error hidden');
 assert.ok($('m2w-check').textContent.includes('иностранный IP не подтверждён'),'M2W verification gap hidden');
 renderProxy('warp',{...p,ready:true,policy_ready:true,policy_error:'',port:4001});
 assert.strictEqual($('warp-badge').textContent,'работает');
 assert.ok($('warp-exit').className.split(' ').includes('ok'));
 renderProxy('warp',{...p,transport_ready:false,policy_ready:false,ready:false});
 assert.strictEqual($('warp-badge').textContent,'поднимается');
 assert.ok(!$('warp-exit').className.split(' ').includes('ok'),'stale exit remains green');
 `
	if out, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("offline render failed: %v\n%s", err, out)
	}
}
