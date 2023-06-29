Package `tools` provides common methods and utilities for Go(lang) based **development** tools that cover:

* integration tests (primarily, `ais/test/*`),
* benchmarks (under `bench/`),
* [`aisloader`](/docs/aisloader.md).

This package and its sub-packages (listed below) should not be linked into production code. Unlike, say, `cmn` package
(that also contains common functions) `devtools` is solely intended for usage with development tools.

Package `devtools` itself contains a spectrum of general-purpose sources and is further sub-divided into packages that group closely related functions:

| Folder | Intended for |
| --- | --- |
| archive | Create, list, and read from [archives](/docs/archive.md) |
| readers | Provides `io.Reader` and related primitives based on: random source, file, [scatter-gather list](/memsys/README.md) |
| tassert | Testing asserts - `CheckFatal`, `Errorf`, `Fatalf`, and other convenient assertions |
| tetl | Common functions used for (and by) ETL tests |
| tlog | Uniform logging for integrations tests |

