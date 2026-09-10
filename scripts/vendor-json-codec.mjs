// Reproducible experimental relocation of Go's existing JSON v1 codec. No new
// reflection mapper/parser is implemented here; behavioral patches are separate.
import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import assert from 'node:assert/strict';
const root=path.resolve(import.meta.dirname,'..');
const goroot=path.resolve(process.argv[2]??'');
assert.equal(fs.readFileSync(path.join(goroot,'VERSION'),'utf8').split('\n')[0],'go1.25.12');
const source=path.join(goroot,'src/encoding/json');
const target=path.join(root,'internal/thirdparty/jsoncodec');
assert(!fs.existsSync(target),'Refuse to overwrite a candidate');
const selected=['decode.go','encode.go','fold.go','indent.go','scanner.go','stream.go','tables.go','tags.go'];
const tests=fs.readdirSync(source).filter(f=>f.endsWith('_test.go')&&!f.startsWith('v2_')&&f!=='bench_test.go');
const hash=b=>crypto.createHash('sha256').update(b).digest('hex');
const manifest={source:'https://go.googlesource.com/go/+/refs/tags/go1.25.12/src/encoding/json/',version:'go1.25.12',status:'EXPERIMENTAL_NOT_ADOPTED',files:{}};
fs.mkdirSync(target,{recursive:true});
for(const name of [...selected,...tests]) {
 const raw=fs.readFileSync(path.join(source,name));
 let content=raw.toString().replace(/^\/\/go:build !goexperiment.jsonv2\n/m,'');
 content=content.replace(/^\/\/go:linkname .+\n/gm,'').replace(/^\s*_ "unsafe" \/\/ for linkname\n/gm,'\n');
 if(name.endsWith('_test.go'))content=content.replaceAll('"encoding/json"','"github.com/openbindings/openbindings-go/internal/thirdparty/jsoncodec"');
 fs.writeFileSync(path.join(target,name),content);
 manifest.files[name]={upstreamSHA256:hash(raw),relocatedSHA256:hash(content)};
}
fs.copyFileSync(path.join(goroot,'LICENSE'),path.join(target,'LICENSE'));
fs.writeFileSync(path.join(target,'UPSTREAM.json'),JSON.stringify(manifest,null,2)+'\n');
console.log(JSON.stringify({relocatedFiles:Object.keys(manifest.files).length,target,status:manifest.status}));
