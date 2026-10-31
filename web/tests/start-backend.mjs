import { mkdtempSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { spawn, spawnSync } from 'node:child_process';

const temporary = mkdtempSync(join(tmpdir(), 'server-fan-e2e-'));
const binary = join(temporary, 'server-fan-control');
const tokenFile = join(temporary, 'admin-token');
writeFileSync(tokenFile, 'e2e-test-admin-token-0123456789abcdef\n', {mode: 0o600});
const build = spawnSync('go', ['build', '-o', binary, './cmd/server-fan-control'], {cwd: resolve('..'), stdio: 'inherit', env: {...process.env, CGO_ENABLED: '1'}});
if (build.status !== 0) { rmSync(temporary, {recursive:true,force:true}); process.exit(build.status ?? 1); }
let server, stopping=false, restarting=false;
const controlFile=process.env.SERVER_FAN_E2E_CONTROL_FILE;
if(controlFile) writeFileSync(controlFile,String(process.pid),{mode:0o600});
function start() {
  server=spawn(binary,['--mock','--listen','127.0.0.1:18080','--config',join(temporary,'config.json'),'--admin-token-file',tokenFile],{stdio:'inherit',env:{...process.env,FAN_CONTROL_IPMI_ENABLED:'true',FAN_CONTROL_SMART_ENABLED:'true'}});
  server.on('exit',code=>{
    if(restarting && !stopping) { restarting=false; start(); return; }
    rmSync(temporary,{recursive:true,force:true});
    if(controlFile) rmSync(controlFile,{force:true});
    process.exit(code ?? 0);
  });
}
function stop() { if(!stopping) { stopping=true; server?.kill('SIGTERM'); } }
process.on('SIGTERM',stop); process.on('SIGINT',stop);
// Test harness only: restart the child while retaining its persistent directory.
process.on('SIGUSR2',()=>{if(!stopping && !restarting){restarting=true;server?.kill('SIGTERM');}});
start();
