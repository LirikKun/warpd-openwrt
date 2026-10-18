package main

import (
 "context"
 "encoding/json"
 "io"
 "os"
 "os/exec"
 "path/filepath"
 "reflect"
 "strconv"
 "strings"
 "syscall"
 "testing"
 "time"
)

func TestIndependentFDChild(t *testing.T) {
 if os.Getenv("RELAY_FD_CHILD") != "1" { return }
 control := os.Getenv("RELAY_FD_CONTROL")
 if !reflect.DeepEqual(os.Args[len(os.Args)-5:], []string{"register", "-a", "account.json", "-fresh", "-plain"}) { os.Exit(31) }
 cwd, e := os.Stat("."); if e != nil || cwd.Mode().Perm()!=0700 { os.Exit(32) }
 output,e := os.Lstat("account.json"); if e != nil || !output.Mode().IsRegular() || output.Mode().Perm()!=0600 { os.Exit(33) }
 st,ok:=output.Sys().(*syscall.Stat_t); if !ok || st.Uid!=uint32(os.Geteuid()) || st.Nlink!=1 { os.Exit(34) }
 var c RelayCredential
 if json.Unmarshal([]byte(credentialFixtureBody()),&c)!=nil { os.Exit(35) }
 raw,_:=json.Marshal(map[string]string{"private_key":c.PrivateKey,"peer_public_key":c.PeerPublicKey,"ipv4":c.TunnelIPv4})
 if os.WriteFile("account.json",raw,0600)!=nil { os.Exit(36) }
 // Only synthetic marker data reaches process output; production must discard it.
 os.Stdout.WriteString("SYNTHETIC_PRIVATE_OUTPUT_MARKER\n")
 os.Stderr.WriteString("SYNTHETIC_PRIVATE_ERROR_MARKER\n")
 if os.WriteFile(filepath.Join(control,"ready"),[]byte("ready"),0600)!=nil { os.Exit(37) }
 deadline:=time.Now().Add(3*time.Second)
 for { if _,e:=os.Stat(filepath.Join(control,"release"));e==nil { break }; if time.Now().After(deadline) { os.Exit(38) };time.Sleep(time.Millisecond) }
 os.Exit(0)
}

func TestIndependentFDExecAfterRootReplacement(t *testing.T) {
 control:=t.TempDir(); root:=filepath.Join(control,"root"); moved:=filepath.Join(control,"moved"); evil:=filepath.Join(control,"replacement")
 for _,p:=range []string{root,evil} { if e:=os.Mkdir(p,0700);e!=nil { t.Fatal(e) } }
 r,e:=openRelayStageRoot(root); if e!=nil { t.Fatal(e) };defer r.dir.Close()
 s,e:=r.create();if e!=nil { t.Fatal(e) };defer s.cleanup()
 if e=s.createOutput();e!=nil { t.Fatal(e) }
 if e=os.Rename(root,moved);e!=nil { t.Fatal(e) }; if e=os.Symlink(evil,root);e!=nil { t.Fatal(e) }
 exe,e:=os.Executable();if e!=nil { t.Fatal(e) }
 ctx,cancel:=context.WithTimeout(context.Background(),4*time.Second);defer cancel()
 cmd:=exec.CommandContext(ctx,exe,"-test.run=^TestIndependentFDChild$","--","register","-a","account.json","-fresh","-plain")
 cmd.Dir=s.childDir();cmd.Env=append(os.Environ(),"RELAY_FD_CHILD=1","RELAY_FD_CONTROL="+control);cmd.Stdout=io.Discard;cmd.Stderr=io.Discard
 if e=cmd.Start();e!=nil { t.Fatal(e) }; done:=make(chan error,1);go func(){done<-cmd.Wait()}()
 deadline:=time.Now().Add(2*time.Second)
 for { if _,e=os.Stat(filepath.Join(control,"ready"));e==nil { break }; select {case e=<-done:t.Fatalf("child exited before ready: %v",e);default:};if time.Now().After(deadline){t.Fatal("child not ready")};time.Sleep(time.Millisecond) }
 argv,e:=os.ReadFile(filepath.Join("/proc",fmtPid(cmd.Process.Pid),"cmdline"));if e!=nil {t.Fatal(e)}
 // Credential contents never appear in argv; the only credential path is a basename.
 var c RelayCredential;_ = json.Unmarshal([]byte(credentialFixtureBody()),&c)
 for _,secret:=range []string{c.PrivateKey,c.PeerPublicKey,c.TunnelIPv4} {if strings.Contains(string(argv),secret){t.Fatal("credential exposed in argv")}}
 if e=os.WriteFile(filepath.Join(control,"release"),[]byte("x"),0600);e!=nil{t.Fatal(e)};if e=<-done;e!=nil{t.Fatal(e)}
 f,e:=s.openOutput();if e!=nil{t.Fatal(e)};data,e:=io.ReadAll(f);f.Close();if e!=nil||!json.Valid(data){t.Fatal("anchored output not readable")}
 if e=s.cleanup();e!=nil{t.Fatal(e)}
 assertNoRelayStageDirs(t,moved)
 entries,e:=os.ReadDir(evil);if e!=nil||len(entries)!=0{t.Fatal("replacement root touched")}
}
func fmtPid(pid int) string { return strconv.Itoa(pid) }

func TestIndependentFDTrustAndSymlinkAncestor(t *testing.T) {
 control:=t.TempDir(); real:=filepath.Join(control,"real");link:=filepath.Join(control,"link")
 if e:=os.Mkdir(real,0700);e!=nil{t.Fatal(e)};if e:=os.Symlink(real,link);e!=nil{t.Fatal(e)}
 if r,e:=openRelayStageRoot(link);e==nil{r.dir.Close();t.Fatal("symlink root accepted")}
 sub:=filepath.Join(real,"sub");if e:=os.Mkdir(sub,0700);e!=nil{t.Fatal(e)}
 if r,e:=openRelayStageRoot(filepath.Join(link,"sub"));e==nil{r.dir.Close();t.Fatal("symlink ancestor accepted")}
 euid:=uint32(os.Geteuid());other:=euid+1;if other==0{other++}
 if trustedRelayStageAncestor(&syscall.Stat_t{Mode:syscall.S_IFDIR|0700,Uid:other}) {t.Fatal("foreign ancestor owner accepted")}
 if trustedRelayStageRoot(&syscall.Stat_t{Mode:syscall.S_IFDIR|0777,Uid:euid}) {t.Fatal("nonsticky writable root accepted")}
 if !trustedRelayStageRoot(&syscall.Stat_t{Mode:syscall.S_IFDIR|syscall.S_ISVTX|0777,Uid:0}) {t.Fatal("root-owned sticky tmp rejected")}
}
