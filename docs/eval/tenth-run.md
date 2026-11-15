# Tenth run: libraries with a reference implementation (2026-11-15)

The ninth run's specs on influential libraries found mostly behaviours
already reported or by design: a doc's sentence is a weak oracle. This run
took influential, maintained libraries whose results can be checked
against an exact reference, so that a disagreement is a bug, not a
reading of the docs. Tool frozen at `cbb35dc` (with `fmt.Sprint`), the
fixes below made during the run.

| project | stars | reference | specs | model | Go's fuzzer (45 s each) |
|---|---|---|---|---|---|
| [shopspring/decimal](https://github.com/shopspring/decimal) `a5eee10` | 7.5k | `math/big.Rat` | 11 | held (budget-limited) | held, 1.9M–4.2M executions each |
| [tidwall/gjson](https://github.com/tidwall/gjson) `c6b726d` | 15.5k | `encoding/json` | 4 | 3 spurious; 2 after the fixes | held, 0.4M–3.2M |
| [tidwall/sjson](https://github.com/tidwall/sjson) `f0069f8` | 2.7k | `encoding/json`, gjson | 3 | 2 spurious; 2 after the fixes | held, 3.6M–4.5M |

No defect. The three are correct on everything the specs reach.

## The specs

- **decimal.** Decimals drawn as a small coefficient (carries at 9, 49,
  99, 999; 5000005 for half-way digits) times 10^-6 to 10^6, both signs.
  `Add`, `Sub`, `Mul`, `Neg` and `Cmp` are exact; `Round` (5 away from
  zero), `RoundBank`, `RoundCeil`, `RoundFloor`, `RoundUp`, `RoundDown`
  and `Truncate` at -3 to 6 places are exact rounding; `DivRound` rounds
  half away from zero and `Div` to `DivisionPrecision`; `QuoRem` meets its
  documented contract (`d = d2*q + r`, the bounds on `r` by sign) and
  `Mod` is its remainder; `RoundCash` at 5, 10, 25, 50 and 100 cents;
  `Floor`, `Ceil`, `IntPart`, `IsInteger`, `Sign`; `PowInt32` with a
  non-negative exponent is the exact product (0 ** 0 an error, as
  documented); `String` and `StringFixed` parse back; `NewFromString` is
  `big.Rat.SetString` where both accept; `NewFromFloat` is
  `FormatFloat(v, 'f', -1, 64)`; `Float64` is `big.Rat.Float64`.
- **gjson.** `Valid` is `json.Valid` on token soup (escapes, bad escapes,
  leading zeros, truncated literals); for a valid object of distinct keys
  (dots, wildcards, `#`, `@`, `|`, quotes, empty, unicode, escaped with
  `Escape`), `Get` of each key is what `encoding/json` decodes there and
  `Parse(s).Value()` is the whole document; a string's `Str` is the
  decoded string (surrogate pairs, lone surrogates, `\u0000`); `#` and
  index paths on arrays.
- **sjson.** `Set` of a top-level key (dots and colons escaped, numeric
  keys forced with `:`) gives valid JSON that decodes to the object with
  the key set; `Delete` removes it; `SetRaw` with `-1` appends to an
  array.

The default templates on decimal found only documented panics (NaN and
infinities, an unsupported cash interval, division by zero) and two that
need absurd arguments (`Shift` past int32, `NewFromBigRat` at precision
-2^31).

## What the tool got wrong, and fixed

Every spurious verdict named its cause:

- **unsafe casts between strings and byte slices.** gjson and sjson
  convert with `*(*string)(unsafe.Pointer(&b))` and their own
  `stringHeader` / `sliceHeader` structs, not `unsafe.String`; the model
  made each cast a fresh object with havoc fields (one spurious verdict in
  gjson, two in sjson). Now a cast is a view of the same bytes and a header
  an object holding the string or slice ([semantics](../semantics.md)).
- **`reflect.DeepEqual` on decoded JSON.** The JSON model keeps a decoded
  number as its decimal text; `DeepEqual` against a float64 the program
  computed was havoc. Now it compares them as floats. The spurious
  verdicts that remain (two each in gjson and sjson) are numbers past 15
  significant digits, which the JSON model keeps unknown (#251): the specs
  hold `-12345678901234567890` and `9007199254740993` on purpose.
- **math/big's division** got the model stuck in `nat.divBasic` on
  decimal's large values (7 spurious verdicts of default templates), the
  stuck-callee havoc of #218. Not fixed here.

## What this says

A library with an exact reference is the best case for a spec: the oracle
is unambiguous and a spec takes minutes to write. These three passed
everything, with the model and with Go's fuzzer. Well-tested arithmetic
and JSON libraries do not give up bugs to equality checks on small
inputs; the findings of the earlier runs came from what a replay checks
on every call (goroutine leaks) and from documentation that the code no
longer follows.
