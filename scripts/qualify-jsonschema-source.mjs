// Reproduce the private upstream source and separately maintained correction.
import { readFileSync, writeFileSync, readdirSync, mkdirSync, mkdtempSync, existsSync } from "node:fs";
import { resolve, dirname, join } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";
import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import assert from "node:assert/strict";

const root=resolve(dirname(fileURLToPath(import.meta.url)),"..");
const mode=process.argv[2], source=resolve(process.argv[3]??"");
assert(["verify","refresh-patch"].includes(mode));
assert(source.endsWith("github.com/santhosh-tekuri/jsonschema/v6@v6.0.3"));
const target=join(root,"internal/thirdparty/jsonschema"), patch=join(root,"patches/jsonschema-v6.0.3.patch");
const manifest=JSON.parse(readFileSync(join(target,"UPSTREAM.json")));
assert.equal(manifest.version,"v6.0.3");
const hash=b=>createHash("sha256").update(b).digest("hex");
const scratch=mkdtempSync(join(tmpdir(),"ob-jsonschema-reproduce-"));
const put=(p,b)=>{mkdirSync(dirname(p),{recursive:true});writeFileSync(p,b);};
const exec=(args,cwd=scratch)=>{
 const r=spawnSync("git",args,{cwd,encoding:"utf8",maxBuffer:20e6});
 assert(r.status===0||(args[0]==="diff"&&r.status===1),r.stderr);
 return r.stdout;
};
for(const [name,expected]of Object.entries(manifest.files)) {
 const original=readFileSync(join(source,name));
 assert.equal(hash(original),expected.upstreamSHA256,name+" upstream drift");
 const relocated=name.endsWith(".go")?Buffer.from(original.toString().replaceAll('"'+manifest.upstream,'"github.com/openbindings/openbindings-go/internal/thirdparty/jsonschema')):original;
 assert.equal(hash(relocated),expected.relocatedSHA256,name+" relocation drift");
 put(join(scratch,"a",name),relocated);
 put(join(scratch,"b",name),readFileSync(join(target,name)));
}
put(join(scratch,"b/numeric_work.go"),readFileSync(join(target,"numeric_work.go")));
if(mode==="refresh-patch") {
 mkdirSync(dirname(patch),{recursive:true});
 writeFileSync(patch,exec(["diff","--no-index","--no-prefix","a","b"]));
} else {
 exec(["apply","--check",patch],join(scratch,"a"));
 exec(["apply",patch],join(scratch,"a"));
 const files=dir=>readdirSync(dir,{withFileTypes:true}).flatMap(e=>e.isDirectory()?files(join(dir,e.name)):[join(dir,e.name)]);
 const expected=new Set([...Object.keys(manifest.files),"numeric_work.go"]);
 for(const p of files(target))if(p.endsWith(".go")&&!p.endsWith("_test.go"))assert(expected.has(p.slice(target.length+1)),"untracked runtime source "+p);
 for(const name of expected)assert.deepEqual(readFileSync(join(scratch,"a",name)),readFileSync(join(target,name)),name+" correction drift");
}
console.log(JSON.stringify({mode,version:manifest.version,files:Object.keys(manifest.files).length,patchSHA256:hash(readFileSync(patch)),scratch}));
