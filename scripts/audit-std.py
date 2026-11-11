"""Groups scripts/audit-std.sh's compile reports: per package, the
functions of the package itself by status; across packages, the causes
that keep a function from translating completely, by how many functions
each affects, and whether the cause is the language (the compile stage
can close it) or the boundary below Go (runtime, unsafe, assembly, the
operating system), which needs a model of its own."""
import collections, glob, json, os, re, sys

ws = sys.argv[1]
BOUNDARY_PKGS = re.compile(r'^(runtime|internal/|syscall|unsafe|reflect|sync/atomic|os$|os/|time$|crypto/internal|vendor/|weak|unique|iter)')

def callee_pkg(c):
    c = c.lstrip('(*')
    # pkg/path.Func, (pkg/path.T).M -> pkg/path
    m = re.match(r'([\w./-]+?)\.[A-Za-z_]\w*', c)
    return m.group(1) if m else c

def cause(issue):
    s = issue
    s = re.sub(r'^[\w.$*()\[\], ]+?: (?=(builtin|call|conversion|type assertion|func value|lookup|map|nil|==|interface|select|go |defer|store|load|range|unsupported|cannot|no ))', '', s)
    m = re.search(r'builtin (\w+)', s)
    if m: return ('builtin ' + m.group(1), 'language')
    if 'unsafe.Pointer' in s or 'unsafe.' in s: return ('unsafe', 'boundary')
    m = re.search(r'call ((?:\(\*?)?[\w./-]+(?:\.\w+)+\)?(?:\.\w+)?)', s)
    if m and ('unmodeled-call' in s or 'not modelled' in s or 'callee stuck' in s):
        p = callee_pkg(m.group(1))
        kind = 'boundary' if BOUNDARY_PKGS.match(p) else 'language'
        if 'callee stuck' in s:
            return ('callee stuck in ' + p, kind)
        return ('unmodelled call into ' + p, kind)
    if re.search(r'call interface method \(([\w./-]+)\.', s):
        p = re.search(r'call interface method \(([\w./-]+)\.', s).group(1)
        return ('unmodelled interface method of ' + p, 'boundary' if BOUNDARY_PKGS.match(p) else 'language')
    if re.search(r'call t\d+|call \w+: t\d+', s): return ('call through an unknown value', 'language')
    if 'func value' in s and 'opaque' in s: return ('func value not modelled', 'language')
    if 'type assertion' in s: return ('type assertion on a value the model did not build', 'language')
    if 'lookup in map' in s or 'map update' in s or re.search(r'\bmap\b.*knows nothing', s): return ('map the model knows nothing about', 'language')
    if 'nil comparison' in s: return ('nil comparison on an unknown value', 'language')
    if '== on an interface' in s or 'on an interface value the model did not build' in s: return ('== on an interface value the model did not build', 'language')
    if 'interface value' in s and 'opaque' in s: return ('interface value not modelled', 'language')
    if 'conversion' in s: return ('conversion: ' + re.sub(r't\d+', 'tN', s.split(':')[0])[:60], 'language')
    if 'store' in s and 'ignored' in s: return ('store ignored: ' + re.sub(r'\b\w+\b ignored', '', s)[:40], 'language')
    g = re.sub(r'\bt\d+\b', 'tN', s)
    g = re.sub(r'"[^"]*"', '"…"', g)
    g = re.sub(r'\d+', 'N', g)
    return (g[:80], 'language')

status = collections.Counter()
per_pkg = []
causes = collections.defaultdict(set)
kinds = {}
failed = []
for done in sorted(glob.glob(os.path.join(ws, 'pkgs', '*', 'done'))):
    pkg, rc, secs = open(done).read().split()
    d = os.path.dirname(done)
    reps = glob.glob(os.path.join(d, 'ws', 'models', 'gossa', pkg.replace('/', '_').replace('.', '_'), 'report.json'))
    if rc != '0' or not reps:
        tail = open(os.path.join(d, 'log')).read().strip().splitlines()[-1:] or ['']
        failed.append((pkg, rc, tail[0][:160]))
        continue
    r = json.load(open(reps[0]))
    c = collections.Counter(f['status'] for f in r['funcs'])
    status.update(c)
    per_pkg.append((pkg, c, int(secs)))
    for f in r['funcs']:
        if f['status'] == 'complete':
            continue
        for i in f.get('issues') or ['(no reason given)']:
            k, kind = cause(i)
            causes[k].add(pkg + '.' + f['name'])
            kinds[k] = kind

total = sum(status.values())
print('# Standard-library coverage audit\n')
print(f'{len(per_pkg)} packages compiled, {len(failed)} failed; {total} functions: '
      f'{status["complete"]} complete ({100*status["complete"]/max(total,1):.1f}%), '
      f'{status["partial"]} partial, {status["rejected"]} rejected.\n')
lang = {k: v for k, v in causes.items() if kinds[k] == 'language'}
bnd = {k: v for k, v in causes.items() if kinds[k] == 'boundary'}
lang_fns = set().union(*lang.values()) if lang else set()
bnd_fns = set().union(*bnd.values()) if bnd else set()
print(f'Functions not complete because of a language cause: {len(lang_fns)}; a boundary cause: {len(bnd_fns)}; '
      f'only boundary causes: {len(bnd_fns - lang_fns)}.\n')
for title, group in (('Language causes', lang), ('Boundary causes', bnd)):
    print(f'## {title} ({len(group)})\n')
    print('| functions | cause | example |\n|---|---|---|')
    for k, v in sorted(group.items(), key=lambda kv: -len(kv[1]))[:60]:
        print(f'| {len(v)} | {k.replace("|", "\\|")} | {sorted(v)[0]} |')
    print()
print('## Packages\n\n| package | complete | partial | rejected | seconds |\n|---|---|---|---|---|')
for pkg, c, secs in sorted(per_pkg, key=lambda x: -(x[1]['partial'] + x[1]['rejected'])):
    print(f'| {pkg} | {c["complete"]} | {c["partial"]} | {c["rejected"]} | {secs} |')
if failed:
    print('\n## Packages that did not compile\n\n| package | exit | last line |\n|---|---|---|')
    for pkg, rc, tail in failed:
        print(f'| {pkg} | {rc} | {tail.replace("|", "\\|")} |')
