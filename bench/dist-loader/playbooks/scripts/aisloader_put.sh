#!/bin/bash
hostname=$(hostname -a)

outdir=/tmp/aisloader/
sudo rm -rf $outdir
sudo mkdir $outdir

each_size=""
total_size=""
duration=""
ais_proxies=""
ais_port=""
grafana_host=""
workers=""

for arg in "$@"; do
    case "$arg" in
        --ais_proxies=*)
            ais_proxies="${arg#*=}"
            ;;
        --ais_port=*)
            ais_port="${arg#*=}"
            ;;
        --each_size=*)
            each_size="${arg#*=}"
            ;;
        --total_size=*)
            total_size="${arg#*=}"
            ;;
        --grafana_host=*)
            grafana_host="${arg#*=}"
            ;;
        --workers=*)
            workers="${arg#*=}"
            ;;
        *)
            echo "Invalid argument: $arg"
            ;;
    esac
done

# Split comma-separated string list of proxies into an array
# AIS cluster in k8s will load-balance requests
IFS=',' read -ra proxy_list <<< "$ais_proxies"

filename="bench-$each_size-put-"
outfile="$outdir$filename$hostname.json"
bucket="ais://bench_$each_size"

aisloader -ip=${proxy_list[0]} -port=$ais_port -loaderid=$(hostname) -loaderidhashlen=8 -bucket=$bucket -cleanup=false -totalputsize=$total_size -minsize=$each_size -maxsize=$each_size -pctput=100 -json -stats-output $outfile --statsdip=$grafana_host -numworkers=$workers