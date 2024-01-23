#!/bin/bash

## Prerequisites: #################################################################################
# - aistore cluster
# - remote aistore cluster (a.k.a. "remais")
# - optionally, remais bucket (the bucket will be created if doesn't exist)
# - ais (CLI)
#
## Example usage:
## 1st, make sure remote ais cluster is attached, e.g.:
#  $ ais show remote-cluster -H
#  $ JcHy3JUrL  http://127.0.0.1:11080  remais    v9  1  11m22.312048996s
#
## 2nd, run:
#  $./ais/test/scripts/remais-prefetch-latest.sh --bucket ais://@remais/abc     ####################

lorem='Lorem ipsum dolor sit amet, consectetur adipiscing elit, sed do eiusmod tempor incididunt ut labore et dolore magna aliqua. Ut enim ad minim veniam, quis nostrud exercitation ullamco laboris nisi ut aliquip ex ea commodo consequat.'

duis='Duis aute irure dolor in reprehenderit in voluptate velit esse cillum dolore eu fugiat nulla pariatur. Excepteur sint occaecat cupidatat non proident, sunt in culpa qui officia deserunt mollit anim id est laborum. Et harum quidem..'

## Command line options (and their respective defaults)
bucket="ais://@remais/abc"

## constants
sum1="xxhash[ad97df912d23103f]"
sum2="xxhash[ecb5ed42299ea74d]"

while (( "$#" )); do
  case "${1}" in
    --bucket) bucket=$2; shift; shift;;
    *) echo "fatal: unknown argument '${1}'"; exit 1;;
  esac
done

## uncomment for verbose output
## set -x

## must be @remais
[[ "$bucket" == *//@* ]] || { echo "Error: expecting remote ais bucket, got ${bucket}"; exit 1; }

## actual bucket inside remais:
rbucket="ais://$(basename ${bucket})"

## remais must be attached
rendpoint=$(ais show remote-cluster -H | awk '{print $2}')
[[ ! -z "$rendpoint" ]] || { echo "Error: no remote ais clusters"; exit 1; }

## check remote bucket; create if doesn't exist
exists=true
AIS_ENDPOINT=$rendpoint ais show bucket $rbucket -c 1>/dev/null 2>&1 || exists=false
[[ "$exists" == "true" ]] || AIS_ENDPOINT=$rendpoint ais create $rbucket || exit 1

## add it to _this_ cluster's BMD
ais show bucket $bucket --add 1>/dev/null || exit 1

## remember existing bucket 'versioning' settings; disable synchronization if need be
validate=$(ais bucket props show ${bucket} versioning.validate_warm_get -H | awk '{print $2}')
[[ "$validate" == "false"  ]] || ais bucket props set $bucket versioning.validate_warm_get=false
sync=$(ais bucket props show ${bucket} versioning.synchronize -H | awk '{print $2}')
[[ "$sync" == "false"  ]] || ais bucket props set $bucket versioning.synchronize=false

cleanup() {
  rc=$?
  ais object rm "$bucket/lorem-duis" 1>/dev/null 2>&1
  [[ "$validate" == "true"  ]] || ais bucket props set $bucket versioning.validate_warm_get=false 1>/dev/null 2>&1
  [[ "$sync" == "true"  ]] || ais bucket props set $bucket versioning.synchronize=false 1>/dev/null 2>&1
  [[ "$exists" == "true" ]] || ais rmb $bucket -y 1>/dev/null 2>&1
  exit $rc
}

trap cleanup EXIT INT TERM

echo -e
ais show performance counters --regex "(GET-COLD$|VERSION-CHANGE$|DELETE)"
echo -e

echo "1. out-of-band PUT: 1st version"
echo $lorem | AIS_ENDPOINT=$rendpoint ais put - "$rbucket/lorem-duis" 1>/dev/null || exit $?

echo "2. prefetch, and check"
ais prefetch "$bucket/lorem-duis" --wait
checksum=$(ais ls "$bucket/lorem-duis" --cached -H -props checksum | awk '{print $2}')
[[ "$checksum" == "$sum1"  ]] || { echo "FAIL: $checksum != $sum1"; exit 1; }

echo "3. out-of-band PUT: 2nd version (overwrite)"
echo $duis | AIS_ENDPOINT=$rendpoint ais put - "$rbucket/lorem-duis" 1>/dev/null || exit $?

echo "4. prefetch and check (expecting the first version's checksum)"
ais prefetch "$bucket/lorem-duis" --wait
checksum=$(ais ls "$bucket/lorem-duis" --cached -H -props checksum | awk '{print $2}')
[[ "$checksum" != "$sum2"  ]] || { echo "FAIL: $checksum == $sum2"; exit 1; }

echo "5. query cold-get count (statistics)"
cnt1=$(ais show performance counters --regex GET-COLD -H | awk '{sum+=$2;}END{print sum;}')

echo "6. prefetch latest: detect version change and update in-cluster copy"
ais prefetch "$bucket/lorem-duis" --latest --wait
checksum=$(ais ls "$bucket/lorem-duis" --cached -H -props checksum | awk '{print $2}')
[[ "$checksum" == "$sum2"  ]] || { echo "FAIL: $checksum != $sum2"; exit 1; }

echo "7. cold-get counter must increment"
cnt2=$(ais show performance counters --regex GET-COLD -H | awk '{sum+=$2;}END{print sum;}')
[[ $cnt2 == $(($cnt1+1)) ]] || { echo "FAIL: $cnt2 != $(($cnt1+1))"; exit 1; }

echo "8. warm GET must remain \"warm\" and cold-get-count must not increment"
ais get "$bucket/lorem-duis" /dev/null 1>/dev/null
checksum=$(ais ls "$bucket/lorem-duis" --cached -H -props checksum | awk '{print $2}')
[[ "$checksum" == "$sum2"  ]] || { echo "FAIL: $checksum != $sum2"; exit 1; }

cnt3=$(ais show performance counters --regex GET-COLD -H | awk '{sum+=$2;}END{print sum;}')
[[ $cnt3 == $cnt2 ]] || { echo "FAIL: $cnt3 != $cnt2"; exit 1; }

echo "9. out-of-band DELETE"
AIS_ENDPOINT=$rendpoint ais object rm "$rbucket/lorem-duis" 1>/dev/null || exit $?

echo "10. prefetch without '--latest': expecting no changes"
ais prefetch "$bucket/lorem-duis" --wait
checksum=$(ais ls "$bucket/lorem-duis" --cached -H -props checksum | awk '{print $2}')
[[ "$checksum" == "$sum2"  ]] || { echo "FAIL: $checksum != $sum2"; exit 1; }

echo "11. remember 'remote-deleted' counter, enable version synchronization on the bucket"
cnt4=$(ais show performance counters --regex REMOTE-DEL -H | awk '{sum+=$2;}END{print sum;}')
ais bucket props set $bucket versioning.synchronize=true

echo "12. run 'prefetch --latest' one last time, and make sure the object \"disappears\""
ais prefetch "$bucket/lorem-duis" --latest --wait 2>/dev/null
[[ $? == 0 ]] || { echo "FAIL: expecting 'prefetch --wait' to return Ok, got $?"; exit 1; }

echo "13. 'remote-deleted' counter must increment"
cnt5=$(ais show performance counters --regex REMOTE-DEL -H | awk '{sum+=$2;}END{print sum;}')
[[ $cnt5 == $(($cnt4+1)) ]] || { echo "FAIL: $cnt5 != $(($cnt4+1))"; exit 1; }

