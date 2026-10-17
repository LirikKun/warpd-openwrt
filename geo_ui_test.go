package main

import "testing"

func TestGeoUIShowsIndependentObservation(t *testing.T) {
	runUIRenderAssertions(t, `
 const when=new Date().toISOString();
 const p={enabled:true,running:true,transport_ready:true,policy_ready:false,ready:false,port:4005,last_check:when,geo_country:'DE',geo_check:when,geo_error:'independent geo verification failed'};
 renderProxy('m2w',p);
 assert.ok($('m2w-geo').textContent.includes('DE'),'independent country hidden');
 assert.ok($('m2w-geo').textContent.includes(fmtTime(when)),'independent check time hidden');
 assert.equal($('m2w-geo-error').textContent,p.geo_error,'independent failure hidden');
 assert.notEqual($('m2w-policy').className,'ok','unverified independent observation turns policy green');
 `)
}
