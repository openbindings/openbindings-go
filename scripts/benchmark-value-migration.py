#!/usr/bin/env python3
"""Compare the final owned implementation with its baseline and owned codec control.

Creates disposable copies only. No runtime strategy switch or source edit is made
in the candidate checkout. Run after correctness/race checks, on an idle host.
"""
import argparse
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile

p = argparse.ArgumentParser(description=__doc__)
p.add_argument('--go', default='go')
p.add_argument('--baseline', default='71cbd964a8df7a5d2b2981756d351d3a59b86c5d')
p.add_argument('--output', type=Path, required=True)
p.add_argument('--openapi-client', type=Path, required=True)
p.add_argument('--count', type=int, default=3)
p.add_argument('--benchtime', default='300ms')
a = p.parse_args()
repo = Path(__file__).resolve().parent.parent
out = a.output.resolve()
out.mkdir(parents=True, exist_ok=True)
for name in ('baseline', 'owned-codec', 'candidate'):
    if (out / name).exists():
        p.error(f'{out/name} already exists; choose a fresh evidence directory')

archive = subprocess.check_output(['git', 'archive', a.baseline], cwd=repo)
(out / 'baseline').mkdir()
with tarfile.open(fileobj=io.BytesIO(archive)) as source:
    source.extractall(out / 'baseline', filter='data')
for name in ('owned-codec', 'candidate'):
    shutil.copytree(repo, out / name, ignore=shutil.ignore_patterns('.git', 'go.work*', '*.test'))
for rel in ('invoke/value_migration_benchmark_test.go', 'invoke/value_migration_heap_test.go',
            'formats/openapi/value_migration_benchmark_test.go',
            'formats/operationgraph/value_migration_benchmark_test.go'):
    shutil.copy2(repo / rel, out / 'baseline' / rel)

# Preserve generic owned copying; force whole-value bounded codecs only for
# typed projection/construction. All queues, limits, snapshots and ledger code
# are otherwise identical to the candidate. This control is not production code.
path = out / 'owned-codec/internal/value/value.go'
s = path.read_text()
needle = '\troot, err := w.capture(reflect.ValueOf(input), map[visit]bool{}, 1, false, 0)'
assert s.count(needle) == 1
s = s.replace(needle, '''\tvar root any
	switch input.(type) {
	case nil, bool, string, float64, json.Number, map[string]any, []any:
		root, err = w.capture(reflect.ValueOf(input), map[visit]bool{}, 1, false, 0)
	default:
		err = fallback
	}''')
path.write_text(s)
path = out / 'owned-codec/internal/value/construct.go'
s = path.read_text()
needle = '\tif plain {'
assert s.count(needle) == 1
s = s.replace(needle, '\tif plain && (t == reflect.TypeFor[any]() || t == reflect.TypeFor[map[string]any]() || t == reflect.TypeFor[[]any]()) {')
path.write_text(s)

def pin(path):
    return subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=path, text=True).strip()
metadata = {'candidateBase': pin(repo), 'candidateDiff': subprocess.check_output(['git', 'diff', '--stat'], cwd=repo, text=True),
            'baseline': a.baseline, 'openapiClient': pin(a.openapi_client),
            'go': subprocess.check_output([a.go, 'version'], text=True).strip(),
            'order': ['baseline', 'owned-codec', 'candidate'], 'count': a.count, 'benchtime': a.benchtime}
(out / 'run.json').write_text(json.dumps(metadata, indent=2)+'\n')
env = dict(os.environ, GOTOOLCHAIN='local', GOPROXY='off', GOSUMDB='off')
for name in metadata['order']:
    root = out / name
    work = out / (name+'.work')
    work.write_text(f'''go 1.25.13
use (
 "{root}"
 "{root}/formats/openapi"
 "{root}/formats/operationgraph"
 "{a.openapi_client.resolve()}"
)
replace github.com/openbindings/openbindings-go v0.2.0 => "{root}"
replace github.com/openbindings/openapi-client/go v0.1.0 => "{a.openapi_client.resolve()}"
''')
    env['GOWORK'] = str(work)
    with (out / (name+'.txt')).open('w') as log:
        subprocess.run([a.go, 'test', './invoke', './formats/openapi/...', './formats/operationgraph/...',
                        '-run', '^$', '-bench', '^Benchmark.*ValueMigration', '-benchmem',
                        '-benchtime', a.benchtime, '-count', str(a.count)], cwd=root, env=env, stdout=log, stderr=subprocess.STDOUT, check=True)
    with (out / (name+'-heap.txt')).open('w') as log:
        subprocess.run([a.go, 'test', './invoke', '-run', '^TestValueMigrationRetainedHeap$', '-count=1', '-v'],
                       cwd=root, env=dict(env, OB_VALUE_HEAP_PROBE='1'), stdout=log, stderr=subprocess.STDOUT, check=True)
    print(name+' complete', flush=True)
