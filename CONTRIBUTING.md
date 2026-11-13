# Contributing

Issues and pull requests are welcome.

- Run `go test ./...` before pushing. Rendering tests download
  chrome-headless-shell once (about 128MB) into your user cache; to use a
  system Chrome instead, set `PAGEMILL_TEST_BROWSER=/path/to/chrome`.
- Keep the public API small. New Options need a test that proves they change
  the output.
- README claims must match what tests measure. If you change performance,
  update the measured numbers.
