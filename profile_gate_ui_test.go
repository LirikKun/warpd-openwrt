package main

import (
 "strings"
 "testing"
)

func TestProfileGatePolicyNoticeMatchesPublicTCPEnforcement(t *testing.T) {
 for _,locale:=range []struct{language,text,unsupported string}{
  {"ru","SOCKS TCP закрыт до подтверждения допустимого выхода.","UDP и BIND не поддерживаются."},
  {"en","SOCKS TCP is closed until a suitable exit is verified.","UDP and BIND are not supported."},
 } {
  page:=renderWebUI(locale.language)
  if !strings.Contains(page,locale.text)||!strings.Contains(page,locale.unsupported){t.Errorf("%s: missing current public-gate behavior/limitations",locale.language)}
  if strings.Contains(page,"Это оценка, не блокировка трафика")||strings.Contains(page,"This is an assessment, not traffic blocking") {t.Errorf("%s: stale advisory-only policy text",locale.language)}
 }
}
