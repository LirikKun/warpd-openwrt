package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestGeoUIShowsIndependentObservation(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node needed for offline rendering")
	}
	start, end := strings.Index(indexHTML, "function renderProxy("), strings.Index(indexHTML, "function renderScan(")
	if start < 0 || end <= start {
		t.Fatal("render function missing")
	}
	script := `const elements={};const $=id=>elements[id]||(elements[id]={textContent:'',className:'',classList:{add(){},remove(){}}});const location={hostname:'router'};const fmtTime=()=> 'GEO_TIME';` + indexHTML[start:end] + `
 renderProxy('m2w',{enabled:true,running:true,transport_ready:true,policy_ready:false,ready:false,port:4005,geo_country:'DE',geo_check:'2026-10-18T00:00:00Z',geo_error:'independent geo verification failed'});
 const text=$('m2w-check').textContent;const assert=require('assert');assert.ok(text.includes('DE')&&text.includes('GEO_TIME')&&text.includes('independent geo verification failed'),text);`
	if out, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("geo UI failed: %v\n%s", err, out)
	}
}
