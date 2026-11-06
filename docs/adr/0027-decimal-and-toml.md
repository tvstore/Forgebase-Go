# 0027 Decimal and TOML libraries

- Status: Accepted (decided by the working session within G2 of the Go port plan, which the owner approved on 2026-11-06 with *"setuju G1–G8"*; G2 left this choice to P1 and an ADR). The owner can overrule it.
- Date: 2026-11-06

## Context

[ADR 0026](0026-go-implementation.md) (G2) allows one decimal library (`shopspring/decimal` or `cockroachdb/apd/v3`) and one TOML library (`BurntSushi/toml` or `pelletier/go-toml/v2`), and leaves the choice to P1. P1.1 ports the whole of `forgebase-core`, which needs both: the `Decimal` field value, and the loader of DocType, workflow, permission and overlay TOML. The port is a **parity port** (G1): the metadata formats and every validation rule behave as in Rust.

## Decision

### TOML: `github.com/BurntSushi/toml` v1.6.0 (MIT, no dependencies)

The Rust implementation uses the `toml` crate 1.1.8 (TOML spec 1.1.0, `Cargo.lock`); BurntSushi v1.6.0 implements TOML 1.1.0 as well, rejects duplicate keys and bad syntax, and has no dependencies. The difference from `pelletier/go-toml/v2` v2.4.3 (also MIT, no dependencies) is small: go-toml has a built-in `DisallowUnknownFields` and nicer error excerpts, BurntSushi has a smaller surface and a generic-tree decode. Both would do; BurntSushi is chosen for the generic tree below.

**How it is used.** The text is decoded into a generic tree (`map[string]any`) and the schema of the Rust serde structs is applied by a small strict decoder in `internal/core/load.go`: exact key names (no other keys: `deny_unknown_fields` at every level), the value types, the defaults, the integer ranges (`u8`, `u32`, `i64`), a JSON `null` as an absent optional. The proposed `MetaData.Undecoded()` approach is **not** used, because BurntSushi matches struct field names **case-insensitively**: `NAME = "x"` decodes into a field called `Name` and is not reported as undecoded (verified), whereas serde rejects it. Decoding into structs would therefore accept files that Rust refuses.

**What differs from Rust (to be listed in the parity report).** The parse-error text. The `toml` crate prints "TOML parse error at line 3, column 1" with an excerpt; BurntSushi's syntax errors have their own wording, and the schema errors (unknown key, missing key, wrong type, out of range) are produced by the decoder in serde's words without the line number: ``fields[1]: unknown field `extra`, expected one of `name`, `label`, ...``. Every input that Rust refuses as a parse error is refused (this is tested case by case), the error is a `MetaError` of kind `MetaParse` as in Rust, and the message begins with the same `cannot parse doctype definition:`. Nothing parses the text of the message. A caller that needs line numbers for a syntax error gets them from BurntSushi's own message.

### Decimal: an own type in `internal/decimal`, no dependency

The values must behave exactly like `rust_decimal` 1.43.0 (the version in `Cargo.lock`), because the HTTP API accepts and prints `Decimal` fields as strings, and `forgebase-core` checks precision and scale on them. Read from the `rust_decimal` source (reading only, nothing built or run):

| Behaviour | `rust_decimal` 1.43.0 |
|---|---|
| Representation | 96-bit unsigned mantissa (up to 79 228 162 514 264 337 593 543 950 335), a scale 0 to 28, a sign; zero is not negative, except for one case below |
| Accepted text (`FromStr`) | optional leading `+` or `-`; digits with at most one `.` (`.5`, `5.` are fine, `.` alone is not); `_` separators after a first digit (`1_000`, `1._5`); no whitespace, no `inf`/`NaN`; exponent forms `1e3`, `1.2E-5`, `-2.5e+3` through `from_scientific_lossy` when the plain parse fails and the text has an `e` or `E` |
| More than 28 fractional digits | **rounded**, not an error: half up on the 29th digit (`1.234567890123456789012345678949999` gives `...6789`), the rest of the text must still be valid |
| Mantissa beyond 96 bits | with a fractional part: the same rounding at the digit that would overflow (a carry that overflows again is an error, `overflow from mantissa after rounding`); without one: the error `Invalid decimal: overflow from too many digits` |
| Exponent forms | an exponent above 28 is the error `Scale exceeds the maximum precision allowed: N > 28`; a negative exponent that would pass scale 28 rounds the mantissa; a positive exponent multiplies the mantissa and strips trailing zeros (`1e3` is `1000`, `1.50e1` is `15.0`); `12345E28` is `Number exceeds maximum value that can be represented.` |
| `Display` | the scale is kept: `12.50` stays `12.50`, `0.00` stays `0.00`, `-0` and `-0.00` (plain forms) print without the sign (`0`, `0.00`), but an exponent form whose mantissa rounds to zero keeps it: `-0.1e-28` and `-0.4e-28` print `-0.0000000000000000000000000000`; `1e3` prints `1000` |
| Equality and order | numeric: `1.0 == 1.00`, `-0 == 0` |
| Errors | `Invalid decimal: empty`, `no digits found`, `two decimal points`, `unknown character`, `must start lead with a number`, `Failed to parse` (a bad exponent) |

An own type on `math/bits` and `math/big` (about 500 lines: `internal/decimal/decimal.go`) reproduces this exactly. It follows the parser of `rust_decimal` state by state, including a quirk: an underscore at the rounding position is skipped to the next digit only on the 128-bit path of the parser, not on the 64-bit one, and the Go parser reproduces both. `shopspring/decimal` (MIT) and `cockroachdb/apd/v3` (Apache-2.0) are arbitrary-precision: they accept far more than 28 digits and any exponent, and neither knows the 96-bit limit or the rounding rules above. Using either would need a wrapper that re-implements the parse rules and the limits, so the dependency would not remove the code that matters, only add a module (G2: the standard library first; the maintenance policy: no dependency when a small own implementation meets the need).

**Test vectors** are taken from `rust_decimal` itself: `tests/decimal_tests.rs` (the parse, scientific, rounding and underscore tests) and the tests of `src/str.rs` (the rounding position, the overflow and the edge cases), each Go test names its source. `internal/decimal/decimal_test.go` has over a hundred vector entries.

**Known limits of the Go type.** Only what Forgebase uses is implemented: parse, `String`, `Scale`, comparison, `Normalize`, `Neg`, `New`, `FromInt`, `Mantissa`. There is no arithmetic (the core does none). The multiplication of a positive exponent is exact: a product above 96 bits is only possible with a fraction, and then only trailing zeros are dropped (`1.1e28` is `11000000000000000000000000000`), exactly as in `rust_decimal`; a product that does not fit even then is the error `Number exceeds maximum value that can be represented.` (`12345E28`).

### Dates and times

The standard library `time`. A `Date` value is a `time.Time` at midnight UTC (`core.NewDate`), a `Datetime` a `time.Time` with its offset. The core itself parses and formats no date (the Rust core takes `Date` and `OffsetDateTime` values); the text forms (`YYYY-MM-DD`, RFC 3339, microsecond truncation) belong to the HTTP layer and are ported with it in P3.

## Alternatives considered

- **`pelletier/go-toml/v2`.** Equivalent; rejected only because the generic-tree decode of BurntSushi gives the exact serde semantics with less code (see above).
- **`BurntSushi` with struct decoding and `Undecoded()`.** Rejected: the case-insensitive key match (above).
- **`shopspring/decimal` or `cockroachdb/apd/v3`.** Rejected (above): a parity wrapper would be needed anyway.

## Consequences

- `go.mod` gets one direct dependency, `github.com/BurntSushi/toml v1.6.0` (MIT, no dependencies of its own); `go.sum` is committed; the licence check lists it. The [dependency evaluation](../blueprint/dependency-evaluation.md#go-dependencies-adr-0026) records both decisions.
- `internal/core` imports the standard library, `github.com/BurntSushi/toml` and `internal/decimal` (a leaf package without I/O). The import direction of ADR 0026 is unchanged.
- Parse error wording differs from Rust (above). The parity report lists it.
- Anything that later needs decimal arithmetic (reports, aggregates) adds it to `internal/decimal` with `rust_decimal`'s rules, or reads the value from PostgreSQL's `numeric` as text.
