// Mechanical, version-pinned source vendoring. Behavioral corrections remain a
// separately reviewable patch; this does not generate a second schema evaluator.
import { readFileSync, writeFileSync, readdirSync, mkdirSync, existsSync } from "node:fs";
import { resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { createHash } from "node:crypto";
import assert from "node:assert/strict";

const root=resolve(dirname(fileURLToPath(import.meta.url)),"..");
const source=resolve(process.argv[2]??"");
assert(source.endsWith("github.com/santhosh-tekuri/jsonschema/v6@v6.0.3"),"supply the verified Go module-cache v6.0.3 directory");
const target=resolve(root,"internal/thirdparty/jsonschema");
assert(!existsSync(target),"refuse to overwrite an existing maintained vendor tree");
const upstream="github.com/santhosh-tekuri/jsonschema/v6";
const privatePath="github.com/openbindings/openbindings-go/internal/thirdparty/jsonschema";
const files=dir=>readdirSync(dir,{withFileTypes:true}).flatMap(e=>e.isDirectory()?files(resolve(dir,e.name)):[resolve(dir,e.name)]);
const hash=data=>createHash("sha256").update(data).digest("hex"),manifest={version:"v6.0.3",upstream,files:{}};
for(const path of files(source)) {
 const relative=path.slice(source.length+1);
 if(!(relative.startsWith("metaschemas/")||relative==="LICENSE"||relative==="README.md"||relative==="go.mod"||relative.endsWith(".go")&&!relative.endsWith("_test.go")))continue;
 if(relative.startsWith("cmd/")||relative==="go.mod")continue;
 const original=readFileSync(path),content=relative.endsWith(".go")?Buffer.from(original.toString().replaceAll('"'+upstream,'"'+privatePath)):original;
 const destination=resolve(target,relative);mkdirSync(dirname(destination),{recursive:true});writeFileSync(destination,content);
 manifest.files[relative]={upstreamSHA256:hash(original),relocatedSHA256:hash(content)};
}
writeFileSync(resolve(target,"UPSTREAM.json"),JSON.stringify(manifest,null,2)+"\n");
console.log(`vendored ${Object.keys(manifest.files).length} upstream source/license/resource files`);
