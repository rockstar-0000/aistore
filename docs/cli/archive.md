---
layout: post
title: BUCKET
permalink: /docs/cli/archive
redirect_from:
 - /cli/archive.md/
 - /docs/cli/archive.md/
---

# When objects are, in fact, archives (shards)

In this document: commands to read, write, and list *archives* - objects formatted as TAR, TGZ, ZIP, etc. For the most recently updated archival types that AIS supports, please refer to [this source](/cmn/cos/archive.go).

The corresponding subset of CLI commands starts with `ais archive`, from where you can `<TAB-TAB>` to the actual (reading, writing, listing) operation.

```console
$ ais archive --help

NAME:
   ais archive - Archive multiple objects from a given bucket; archive local files and directories; list archived content

USAGE:
   ais archive command [command options] [arguments...]

COMMANDS:
   bucket      archive multiple objects from SRC_BUCKET as (.tar, .tgz or .tar.gz, .zip, .tar.lz4)-formatted shard
   put         archive a file, a directory, or multiple files and/or directories as
               (.tar, .tgz or .tar.gz, .zip, .tar.lz4)-formatted object - aka "shard".
               Both APPEND (to an existing shard) and PUT (new version of the shard) variants are supported.
               Examples:
               - 'local-filename bucket/shard-00123.tar.lz4 --archpath name-in-archive' - append a file to a given shard and name it as specified;
               - 'src-dir bucket/shard-99999.zip -put' - one directory; iff the destination .zip doesn't exist create a new one;
               - '"sys, docs" ais://dst/CCC.tar --dry-run -y -r --archpath ggg/' - dry-run to recursively archive two directories.
               Tips:
               - use '--dry-run' option if in doubt;
               - to archive objects from a local or remote bucket, run 'ais archive bucket', see --help for details.
   get         get a shard, an archived file, or a range of bytes from the above;
               - use '--prefix' to get multiple objects in one shot (empty prefix for the entire bucket)
               - write the content locally with destination options including: filename, directory, STDOUT ('-')
   ls          list archived content (supported formats: .tar, .tgz or .tar.gz, .zip, .tar.lz4)
   gen-shards  generate random (.tar, .tgz or .tar.gz, .zip, .tar.lz4)-formatted objects ("shards"), e.g.:
               - gen-shards 'ais://bucket1/shard-{001..999}.tar' - write 999 random shards (default sizes) to ais://bucket1
               - gen-shards "gs://bucket2/shard-{01..20..2}.tgz" - 10 random gzipped tarfiles to Cloud bucket
               (notice quotation marks in both cases)
```

See also:

> [Append file to archive](/docs/cli/object.md#append-file-to-archive)
> [Archive multiple objects](/docs/cli/object.md#archive-multiple-objects)

## Table of Contents
- [Archive multiple objects](#archive-multiple-objects)
- [List archive content](#list-archive-content)
- [Append file to archive](#append-file-to-archive)

## Archive multiple objects

Archive multiple objects from the source bucket.

```console
$ ais archive put --help
NAME:
   ais archive put - put multi-object (.tar, .tgz or .tar.gz, .zip, .tar.lz4) archive

USAGE:
   ais archive put [command options] SRC_BUCKET DST_BUCKET/OBJECT_NAME

OPTIONS:
   --template value   template to match object names; may contain prefix with zero or more ranges (with optional steps and gaps), e.g.:
                      --template 'dir/subdir/'
                      --template 'shard-{1000..9999}.tar'
                      --template "prefix-{0010..0013..2}-gap-{1..2}-suffix"
                      --template "prefix-{0010..9999..2}-suffix"
   --list value       comma-separated list of object names, e.g.:
                      --list 'o1,o2,o3'
                      --list "abc/1.tar, abc/1.cls, abc/1.jpeg"
   --include-src-bck  prefix names of archived objects with the source bucket name
   --append           if destination object ("archive", "shard") already exists, append to it
                      (instead of creating a new one)
   --cont-on-err      keep running archiving xaction in presence of errors in a any given multi-object transaction
```

The operation accepts either an explicitly defined *list* or template-defined *range* of object names (to archive).

As such, `archive put` is one of the supported [multi-object operations](/docs/cli/object.md#operations-on-lists-and-ranges).

Also note that `ais put` command with its `--archive` option provides an alternative way to archive multiple objects:

* [`ais put BUCKET/OBJECT --archive`](/docs/cli/object.md##archive-multiple-objects)

For the most recently updated list of supported archival formats, please see:

* [this source](https://github.com/NVIDIA/aistore/blob/master/cmn/cos/archive.go).

### Examples

1. Archive list of objects from a given bucket:

```console
$ ais archive put ais://bck/arch.tar --list obj1,obj2
Archiving "ais://bck/arch.tar" ...
```

Resulting `ais://bck/arch.tar` contains objects `ais://bck/obj1` and `ais://bck/obj2`.

2. Archive objects from a different bucket, use template (range):

```console
$ ais archive put ais://src ais://dst/arch.tar --template "obj-{0..9}"

Archiving "ais://dst/arch.tar" ...
```

`ais://dst/arch.tar` now contains 10 objects from bucket `ais://src`: `ais://src/obj-0`, `ais://src/obj-1` ... `ais://src/obj-9`.

3. Archive 3 objects and then append 2 more:

```console
$ ais archive put ais://bck/arch1.tar --template "obj{1..3}"
Archived "ais://bck/arch1.tar" ...
$ ais archive ls ais://bck/arch1.tar
NAME                     SIZE
arch1.tar                31.00KiB
    arch1.tar/obj1       9.26KiB
    arch1.tar/obj2       9.26KiB
    arch1.tar/obj3       9.26KiB

$ ais archive put ais://bck/arch1.tar --template "obj{4..5}" --append
Archived "ais://bck/arch1.tar"

$ ais archive ls ais://bck/arch1.tar
NAME                     SIZE
arch1.tar                51.00KiB
    arch1.tar/obj1       9.26KiB
    arch1.tar/obj2       9.26KiB
    arch1.tar/obj3       9.26KiB
    arch1.tar/obj4       9.26KiB
    arch1.tar/obj5       9.26KiB
```

## List archive content

`ais archive ls BUCKET/OBJECT`

Display an archive content as a tree, where the root is the archive name and leaves are files inside the archive.
The filenames are always sorted alphabetically.

### Options

| Name | Type | Description | Default |
| --- | --- | --- | --- |
| `--props` | `string` | Comma-separated properties to return with object names | `"size"`
| `--all` | `bool` | Show all objects, including misplaced, duplicated, etc. | `false` |

### Examples

```console
$ ais archive ls ais://bck/arch.tar
NAME                SIZE
arch.tar            4.5KiB
    arch.tar/obj1   1.0KiB
    arch.tar/obj2   1.0KiB
```

## Append file to archive

Add a file to an existing archive.

### Options

| Name | Type | Description | Default |
| --- | --- | --- | --- |
| `--archpath` | `string` | Path inside the archive for the new file | `""`

**NOTE:** the option `--archpath` cannot be omitted (MUST be specified).

### Example 1

```console
# contents _before_:
$ ais archive ls ais://bck/arch.tar
NAME                SIZE
arch.tar            4.5KiB
    arch.tar/obj1   1.0KiB
    arch.tar/obj2   1.0KiB

# Do append:
$ ais archive /tmp/obj1.bin ais://bck/arch.tar --archpath bin/obj1
APPEND "/tmp/obj1.bin" to object "ais://bck/arch.tar[/bin/obj1]"

# contents _after_:
$ ais archive ls ais://bck/arch.tar
NAME                    SIZE
arch.tar                6KiB
    arch.tar/bin/obj1   2.KiB
    arch.tar/obj1       1.0KiB
    arch.tar/obj2       1.0KiB
```

### Example 2

```console
# contents _before_:

$ ais archive ls ais://nnn/shard-2.tar
NAME                                             SIZE
shard-2.tar                                      5.50KiB
    shard-2.tar/0379f37cbb0415e7eaea-3.test      1.00KiB
    shard-2.tar/504c563d14852368575b-5.test      1.00KiB
    shard-2.tar/c7bcb7014568b5e7d13b-4.test      1.00KiB

# Do append
# Note that --archpath can specify fully qualified name of the destination

$ ais archive put LICENSE ais://nnn/shard-2.tar --archpath shard-2.tar/license.test
APPEND "/go/src/github.com/NVIDIA/aistore/LICENSE" to "ais://nnn/shard-2.tar[/shard-2.tar/license.test]"

# contents _after_:
$ ais archive ls ais://nnn/shard-2.tar
NAME                                             SIZE
shard-2.tar                                      7.50KiB
    shard-2.tar/0379f37cbb0415e7eaea-3.test      1.00KiB
    shard-2.tar/504c563d14852368575b-5.test      1.00KiB
    shard-2.tar/c7bcb7014568b5e7d13b-4.test      1.00KiB
    shard-2.tar/license.test                     1.05KiB
```

## Generate shards

`ais archive gen-shards "BUCKET/TEMPLATE.EXT"`

Put randomly generated shards that can be used for dSort testing.
The `TEMPLATE` must be bash-like brace expansion (see examples) and `.EXT` must be one of: `.tar`, `.tar.gz`.

**Warning**: Remember to always quote the argument (`"..."`) otherwise the brace expansion will happen in terminal.

### Options

| Flag | Type | Description | Default |
| --- | --- | --- | --- |
| `--fsize` | `string` | Single file size inside the shard, can end with size suffix (k, MB, GiB, ...) | `1024`  (`1KB`)|
| `--fcount` | `int` | Number of files inside single shard | `5` |
| `--cleanup` | `bool` | When set, the old bucket will be deleted and created again | `false` |
| `--conc` | `int` | Limits number of concurrent `PUT` requests and number of concurrent shards created | `10` |

### Examples

#### Generate shards with varying numbers of files and file sizes

Generate 10 shards each containing 100 files of size 256KB and put them inside `ais://dsort-testing` bucket (creates it if it does not exist).
Shards will be named: `shard-0.tar`, `shard-1.tar`, ..., `shard-9.tar`.

```console
$ ais archive gen-shards "ais://dsort-testing/shard-{0..9}.tar" --fsize 262144 --fcount 100
Shards created: 10/10 [==============================================================] 100 %
$ ais ls ais://dsort-testing
NAME		SIZE		VERSION
shard-0.tar	25.05MiB	1
shard-1.tar	25.05MiB	1
shard-2.tar	25.05MiB	1
shard-3.tar	25.05MiB	1
shard-4.tar	25.05MiB	1
shard-5.tar	25.05MiB	1
shard-6.tar	25.05MiB	1
shard-7.tar	25.05MiB	1
shard-8.tar	25.05MiB	1
shard-9.tar	25.05MiB	1
```

#### Generate shards using custom naming template

Generates 100 shards each containing 5 files of size 256KB and put them inside `dsort-testing` bucket.
Shards will be compressed and named: `super_shard_000_last.tgz`, `super_shard_001_last.tgz`, ..., `super_shard_099_last.tgz`

```console
$ ais archive gen-shards "ais://dsort-testing/super_shard_{000..099}_last.tar" --fsize 262144 --cleanup
Shards created: 100/100 [==============================================================] 100 %
$ ais ls ais://dsort-testing
NAME				SIZE	VERSION
super_shard_000_last.tgz	1.25MiB	1
super_shard_001_last.tgz	1.25MiB	1
super_shard_002_last.tgz	1.25MiB	1
super_shard_003_last.tgz	1.25MiB	1
super_shard_004_last.tgz	1.25MiB	1
super_shard_005_last.tgz	1.25MiB	1
super_shard_006_last.tgz	1.25MiB	1
super_shard_007_last.tgz	1.25MiB	1
...
```
