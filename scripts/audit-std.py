"""Groups scripts/audit-std.sh's compile reports: per package, the
functions of the package itself by status; across packages, the causes
that keep a function from translating completely, by how many functions
each affects, and whether the cause is the language (the compile stage
can close it) or the boundary below Go (runtime, unsafe, assembly, the
operating system), which needs a model of its own."""
import collections, glob, json, os, re, sys

ws = sys.argv[1]
# The boundary below Go: packages whose code is the runtime, assembly,
# unsafe memory or the operating system (the others are plain Go, which
# the compile stage translates once it reaches them).
BOUNDARY_PKGS = re.compile(r'^(runtime|internal/(abi|runtime|poll|syscall|godebug|race|cpu|reflectlite|bytealg|sync|goarch|goos|asan|msan|testlog|filepathlite)|syscall|unsafe|reflect|sync/atomic|os|os/exec|os/signal|time|weak|unique|iter|crypto/internal/(boring|sysrand|rand|fips140deps|entropy|impl|randutil))(/|$)')

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
    m = re.search(r'call to (?:interface method )?((?:\(\*?)?[\w./-]+(?:\.\w+)+\)?(?:\.\w+)?) ignored', s)
    if m:
        p = callee_pkg(m.group(1))
        return ('unmodelled call into ' + p, 'boundary' if BOUNDARY_PKGS.match(p) else 'language')
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
    r['funcs'] = r.get('funcs') or []
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

# Families: each function not complete goes to the first family, in
# this order, that one of its causes belongs to.
FAMILIES = [
    ('excluded', 'test helpers (take a testing type; never targets)'),
    ('language', 'language: the compile stage does not model it yet'),
    ('boundary', 'boundary: runtime, unsafe, assembly (no body), the OS'),
    ('depth', 'a dependency function -deps did not reach (refinement translates it on demand)'),
    ('analysis', 'a value of unknown origin, compiled out of context (an input supplies it in a run)'),
]

def family(k, kind):
    if k.startswith('test helper'):
        return 'excluded'
    if k == 'no body' or kind == 'boundary' or 'internal/cpu' in k or 'godebug' in k or 'KeepAlive' in k:
        return 'boundary'
    if k.startswith('unmodelled call into ') or k.startswith('callee stuck in '):
        return 'depth'
    if k.startswith('unmodelled interface method of '):
        return 'analysis'
    if k in ('type assertion on a value the model did not build', 'nil comparison on an unknown value',
             'map the model knows nothing about', '== on an interface value the model did not build',
             'interface value not modelled', 'func value not modelled', 'call through an unknown value') \
            or 'float the model does not know' in k or k.startswith('load ') and 'havoc' in k:
        return 'analysis'
    return 'language'

fam_of = {}
for k, fns in causes.items():
    f = family(k, kinds[k])
    for fn in fns:
        order = [x[0] for x in FAMILIES]
        if fn not in fam_of or order.index(f) < order.index(fam_of[fn]):
            fam_of[fn] = f
fam_count = collections.Counter(fam_of.values())
fam_causes = collections.defaultdict(collections.Counter)
for k, fns in causes.items():
    for fn in fns:
        if fam_of[fn] == family(k, kinds[k]):
            fam_causes[fam_of[fn]][k] += 1

total = sum(status.values())
print('# Standard-library coverage audit\n')
print(f'{len(per_pkg)} packages compiled, {len(failed)} failed; {total} functions: '
      f'{status["complete"]} complete ({100*status["complete"]/max(total,1):.1f}%), '
      f'{status["partial"]} partial, {status["rejected"]} rejected.\n')
considered = total - fam_count['excluded']
print('## Outlook\n')
print(f'Of {considered} functions (test helpers left out): {status["complete"]} complete '
      f'({100*status["complete"]/max(considered,1):.1f}%); the others, by what keeps them from it:\n')
print('| functions | share | family |\n|---|---|---|')
for key, title in FAMILIES[1:]:
    print(f'| {fam_count[key]} | {100*fam_count[key]/max(considered,1):.1f}% | {title} |')
print()
for key, title in FAMILIES[1:3]:
    print(f'### {title}\n\n| functions | cause |\n|---|---|')
    for k, n in fam_causes[key].most_common(40):
        print(f'| {n} | {k.replace("|", "\\|")} |')
    print()
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
