// Package aisloader
/*
 * Copyright (c) 2018-2024, NVIDIA CORPORATION. All rights reserved.
 */

// AIS loader (aisloader) is a tool to measure storage performance. It's a load
// generator that can be used to benchmark and stress-test AIStore
// or any S3-compatible backend.
// In fact, aisloader can list, write, and read S3(*) buckets _directly_, which
// makes it quite useful, convenient, and easy to use benchmark tool to compare
// storage performance with aistore in front of S3 vs _without_.
//
// (*) aisloader can be further easily extended to work directly with any
// Cloud storage including, but not limited to, aistore-supported GCP and Azure.
//
// In addition, `aisloader` generates synthetic workloads that mimic training and
// inference workloads - the capability that allows to run benchmarks in isolation
// avoiding compute-side bottlenecks and the associated complexity (to analyze those).
//
// For usage, run: `aisloader`, or `aisloader usage`, or `aisloader --help`,
// or see examples.go.

package aisloader

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/NVIDIA/aistore/api"
	"github.com/NVIDIA/aistore/api/apc"
	"github.com/NVIDIA/aistore/api/authn"
	"github.com/NVIDIA/aistore/api/env"
	"github.com/NVIDIA/aistore/bench/tools/aisloader/namegetter"
	"github.com/NVIDIA/aistore/bench/tools/aisloader/stats"
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/cmn/atomic"
	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/NVIDIA/aistore/cmn/debug"
	"github.com/NVIDIA/aistore/cmn/mono"
	"github.com/NVIDIA/aistore/core/meta"
	"github.com/NVIDIA/aistore/ext/etl"
	"github.com/NVIDIA/aistore/hk"
	"github.com/NVIDIA/aistore/memsys"
	"github.com/NVIDIA/aistore/stats/statsd"
	"github.com/NVIDIA/aistore/tools/readers"
	"github.com/NVIDIA/aistore/tools/tetl"
	"github.com/NVIDIA/aistore/xact"
	"github.com/OneOfOne/xxhash"
	"github.com/aws/aws-sdk-go/service/s3"
	jsoniter "github.com/json-iterator/go"
)

const (
	myName           = "loader"
	randomObjNameLen = 32

	wo2FreeSize  = 4096
	wo2FreeDelay = 3*time.Second + time.Millisecond

	ua = "aisloader"

	defaultClusterIP   = "localhost"
	defaultClusterIPv4 = "127.0.0.1"
)

type (
	params struct {
		seed              int64 // random seed; UnixNano() if omitted
		putSizeUpperBound int64
		minSize           int64
		maxSize           int64
		readOff           int64 // read offset
		readLen           int64 // read length
		loaderCnt         uint64
		maxputs           uint64
		putShards         uint64
		statsdPort        int
		statsShowInterval int
		putPct            int // % of puts, rest are gets
		numWorkers        int
		batchSize         int // batch is used for bootstraping(list) and delete
		loaderIDHashLen   uint
		numEpochs         uint

		duration DurationExt // stop after the run for at least that much

		bp   api.BaseParams
		smap *meta.Smap

		bck    cmn.Bck
		bProps cmn.Bprops

		loaderID             string // used with multiple loader instances generating objects in parallel
		proxyURL             string
		readerType           string
		tmpDir               string // used only when usingFile
		statsOutput          string
		cksumType            string
		statsdIP             string
		bPropsStr            string
		putSizeUpperBoundStr string // stop after writing that amount of data
		minSizeStr           string
		maxSizeStr           string
		readOffStr           string // read offset
		readLenStr           string // read length
		subDir               string
		tokenFile            string
		fileList             string // local file that contains object names (an alternative to running list-objects)

		etlName     string // name of a ETL to apply to each object. Omitted when etlSpecPath specified.
		etlSpecPath string // Path to a ETL spec to apply to each object.

		cleanUp BoolExt // cleanup i.e. remove and destroy everything created during bench

		statsdProbe   bool
		getLoaderID   bool
		randomObjName bool
		randomProxy   bool
		uniqueGETs    bool
		skipList      bool // true: skip listing objects before running 100% PUT workload (see also fileList)
		verifyHash    bool // verify xxhash during get
		getConfig     bool // true: load control plane (read proxy config)
		jsonFormat    bool
		stoppable     bool // true: terminate by Ctrl-C
		dryRun        bool // true: print configuration and parameters that aisloader will use at runtime
		traceHTTP     bool // true: trace http latencies as per httpLatencies & https://golang.org/pkg/net/http/httptrace
	}

	// sts records accumulated puts/gets information.
	sts struct {
		put       stats.HTTPReq
		get       stats.HTTPReq
		getConfig stats.HTTPReq
		statsd    stats.Metrics
	}

	jsonStats struct {
		Start      time.Time     `json:"start_time"`   // time current stats started
		Cnt        int64         `json:"count,string"` // total # of requests
		Bytes      int64         `json:"bytes,string"` // total bytes by all requests
		Errs       int64         `json:"errors"`       // number of failed requests
		Latency    int64         `json:"latency"`      // Average request latency in nanoseconds
		Duration   time.Duration `json:"duration"`
		MinLatency int64         `json:"min_latency"`
		MaxLatency int64         `json:"max_latency"`
		Throughput int64         `json:"throughput,string"`
	}
)

var (
	runParams        *params
	rnd              *rand.Rand
	intervalStats    sts
	accumulatedStats sts
	bucketObjsNames  namegetter.ObjectNameGetter
	statsPrintHeader = "%-10s%-6s%-22s\t%-22s\t%-36s\t%-22s\t%-10s\n"
	statsdC          *statsd.Client
	getPending       int64
	putPending       int64
	traceHTTPSig     atomic.Bool

	flagUsage   bool
	flagVersion bool
	flagQuiet   bool

	etlInitSpec *etl.InitSpecMsg
	etlName     string

	useRandomObjName bool
	objNameCnt       atomic.Uint64

	suffixIDMaskLen uint
	suffixID        uint64

	numGets atomic.Int64

	gmm      *memsys.MMSA
	stopping atomic.Bool

	ip          string
	port        string
	envEndpoint string

	s3svc *s3.S3 // s3 client - see s3ListObjects

	s3Endpoint string
	s3Profile  string

	loggedUserToken string
)

var (
	workCh  chan *workOrder
	resCh   chan *workOrder
	wo2Free []*workOrder
)

var _version, _buildtime string

// main function
func Start(version, buildtime string) (err error) {
	_version, _buildtime = version, buildtime

	// global and parsed/validated
	runParams = &params{}

	// discard flags of imported packages
	// define and add aisloader's own flags
	// parse flags
	f := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	addCmdLine(f, runParams)

	// validate and finish initialization
	if err = _init(runParams); err != nil {
		return err
	}

	// print arguments unless quiet
	if !flagQuiet && !runParams.getLoaderID {
		printArguments(f)
	}

	if runParams.getLoaderID {
		fmt.Printf("0x%x\n", suffixID)
		if useRandomObjName {
			fmt.Printf("Warning: loaderID 0x%x used only for StatsD, not for object names!\n", suffixID)
		}
		return nil
	}

	// If none of duration, epochs, or put upper bound is specified, it is a no op.
	// Note that stoppable prevents being a no op
	// This can be used as a cleanup only run (no put no get).
	if runParams.duration.Val == 0 {
		if runParams.putSizeUpperBound == 0 && runParams.numEpochs == 0 && !runParams.stoppable {
			if runParams.cleanUp.Val {
				cleanup()
			}
			return nil
		}

		runParams.duration.Val = time.Duration(math.MaxInt64)
	}

	if runParams.readerType == readers.TypeFile {
		if err := cos.CreateDir(runParams.tmpDir + "/" + myName); err != nil {
			return fmt.Errorf("failed to create local test directory %q, err = %s", runParams.tmpDir, err.Error())
		}
	}

	// usage is currently limited to selecting a random proxy (gateway)
	// to access aistore (done for every I/O request)
	if runParams.randomProxy {
		runParams.smap, err = api.GetClusterMap(runParams.bp)
		if err != nil {
			return fmt.Errorf("failed to get cluster map: %v", err)
		}
	}
	loggedUserToken = authn.LoadToken(runParams.tokenFile)
	runParams.bp.Token = loggedUserToken
	runParams.bp.UA = ua
	var created bool
	if err := setupBucket(runParams, &created); err != nil {
		return err
	}

	if isDirectS3() {
		initS3Svc()
	}

	// list objects, or maybe not
	if created {
		if runParams.putPct < 100 {
			return errors.New("new bucket, expecting 100% PUT")
		}
		bucketObjsNames = &namegetter.RandomNameGetter{}
		bucketObjsNames.Init([]string{}, rnd)
	} else if !runParams.getConfig && !runParams.skipList {
		if err := listObjects(); err != nil {
			return err
		}

		objsLen := bucketObjsNames.Len()
		if runParams.putPct == 0 && objsLen == 0 {
			return errors.New("nothing to read, the bucket is empty")
		}

		fmt.Printf("Found %s existing object%s\n\n", cos.FormatBigNum(objsLen), cos.Plural(objsLen))
	} else {
		bucketObjsNames = &namegetter.RandomNameGetter{}
		bucketObjsNames.Init([]string{}, rnd)
	}

	printRunParams(runParams)
	if runParams.dryRun { // dry-run so just print the configurations and exit
		os.Exit(0)
	}

	if runParams.cleanUp.Val {
		fmt.Printf("BEWARE: cleanup is enabled, bucket %s will be destroyed upon termination!\n", runParams.bck)
		time.Sleep(time.Second)
	}

	host, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("failed to get host name: %s", err.Error())
	}
	prefixC := fmt.Sprintf("aisloader.%s-%x", host, suffixID)
	statsdC, err = statsd.New(runParams.statsdIP, runParams.statsdPort, prefixC, runParams.statsdProbe)
	if err != nil {
		fmt.Printf("%s", "Failed to connect to StatsD server")
		time.Sleep(time.Second)
	}
	defer statsdC.Close()

	// init housekeeper and memsys;
	// empty config to use memsys constants;
	// alternatively: "memsys": { "min_free": "2gb", ... }
	hk.Init(&stopping)
	go hk.DefaultHK.Run()
	hk.WaitStarted()

	config := &cmn.Config{}
	config.Log.Level = "3"
	memsys.Init(prefixC, prefixC, config)
	gmm = memsys.PageMM()
	gmm.RegWithHK()

	if etlInitSpec != nil {
		fmt.Println(now(), "Starting ETL...")
		etlName, err = api.ETLInit(runParams.bp, etlInitSpec)
		if err != nil {
			return fmt.Errorf("failed to initialize ETL: %v", err)
		}
		fmt.Println(now(), etlName, "started")

		defer func() {
			fmt.Println(now(), "Stopping ETL", etlName)
			if err := api.ETLStop(runParams.bp, etlName); err != nil {
				fmt.Printf("%s Failed to stop ETL %s: %v\n", now(), etlName, err)
				return
			}
			fmt.Println(now(), etlName, "stopped")
		}()
	}

	workCh = make(chan *workOrder, runParams.numWorkers)
	resCh = make(chan *workOrder, runParams.numWorkers)
	wg := &sync.WaitGroup{}
	for i := 0; i < runParams.numWorkers; i++ {
		wg.Add(1)
		go worker(workCh, resCh, wg, &numGets)
	}
	if runParams.putPct != 0 {
		wo2Free = make([]*workOrder, 0, wo2FreeSize)
	}

	timer := time.NewTimer(runParams.duration.Val)

	var statsTicker *time.Ticker
	if runParams.statsShowInterval == 0 {
		statsTicker = time.NewTicker(math.MaxInt64)
	} else {
		statsTicker = time.NewTicker(time.Second * time.Duration(runParams.statsShowInterval))
	}

	tsStart := time.Now()
	intervalStats = newStats(tsStart)
	accumulatedStats = newStats(tsStart)

	statsWriter := os.Stdout

	if runParams.statsOutput != "" {
		f, err := cos.CreateFile(runParams.statsOutput)
		if err != nil {
			fmt.Println("Failed to create stats out file")
		}

		statsWriter = f
	}

	osSigChan := make(chan os.Signal, 2)
	if runParams.stoppable {
		signal.Notify(osSigChan, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	} else {
		signal.Notify(osSigChan, syscall.SIGHUP)
	}

	preWriteStats(statsWriter, runParams.jsonFormat)

	// Get the workers started
	for i := 0; i < runParams.numWorkers; i++ {
		if err = postNewWorkOrder(); err != nil {
			break
		}
	}
	if err != nil {
		goto Done
	}

MainLoop:
	for {
		if runParams.putSizeUpperBound != 0 &&
			accumulatedStats.put.TotalBytes() >= runParams.putSizeUpperBound {
			break
		}

		if runParams.numEpochs > 0 { // if defined
			if numGets.Load() > int64(runParams.numEpochs)*int64(bucketObjsNames.Len()) {
				break
			}
		}

		// Prioritize showing stats otherwise we will dropping the stats intervals.
		select {
		case <-statsTicker.C:
			accumulatedStats.aggregate(&intervalStats)
			writeStats(statsWriter, runParams.jsonFormat, false /* final */, &intervalStats, &accumulatedStats)
			sendStatsdStats(&intervalStats)
			intervalStats = newStats(time.Now())
		default:
			break
		}

		select {
		case <-timer.C:
			break MainLoop
		case wo := <-resCh:
			completeWorkOrder(wo, false)
			if runParams.statsShowInterval == 0 && runParams.putSizeUpperBound != 0 {
				accumulatedStats.aggregate(&intervalStats)
				intervalStats = newStats(time.Now())
			}
			if err := postNewWorkOrder(); err != nil {
				fmt.Fprintln(os.Stderr, err.Error())
				break MainLoop
			}
		case <-statsTicker.C:
			accumulatedStats.aggregate(&intervalStats)
			writeStats(statsWriter, runParams.jsonFormat, false /* final */, &intervalStats, &accumulatedStats)
			sendStatsdStats(&intervalStats)
			intervalStats = newStats(time.Now())
		case sig := <-osSigChan:
			switch sig {
			case syscall.SIGHUP:
				msg := "Detailed latency info is "
				if traceHTTPSig.Toggle() {
					msg += "disabled"
				} else {
					msg += "enabled"
				}
				fmt.Println(msg)
			default:
				if runParams.stoppable {
					break MainLoop
				}
			}
		}
	}

Done:
	timer.Stop()
	statsTicker.Stop()
	close(workCh)
	wg.Wait() // wait until all workers complete their work

	// Process left over work orders
	close(resCh)
	for wo := range resCh {
		completeWorkOrder(wo, true)
	}

	finalizeStats(statsWriter)
	fmt.Printf("Stats written to %s\n", statsWriter.Name())
	if runParams.cleanUp.Val {
		cleanup()
	}

	fmt.Printf("\nActual run duration: %v\n", time.Since(tsStart))

	return err
}

func addCmdLine(f *flag.FlagSet, p *params) {
	f.BoolVar(&flagUsage, "usage", false, "Show command-line options, usage, and examples")
	f.BoolVar(&flagVersion, "version", false, "Show aisloader version")
	f.BoolVar(&flagQuiet, "quiet", false, "When starting to run, do not print command line arguments, default settings, and usage examples")
	f.DurationVar(&cargs.Timeout, "timeout", 10*time.Minute, "Client HTTP timeout - used in LIST/GET/PUT/DELETE")
	f.IntVar(&p.statsShowInterval, "statsinterval", 10, "Interval in seconds to print performance counters; 0 - disabled")
	f.StringVar(&p.bck.Name, "bucket", "", "Bucket name or bucket URI. If empty, a bucket with random name will be created")
	f.StringVar(&p.bck.Provider, "provider", apc.AIS,
		"ais - for AIS bucket, \"aws\", \"azure\", \"gcp\", \"hdfs\"  for Azure, Amazon, Google, and HDFS clouds respectively")

	f.StringVar(&ip, "ip", defaultClusterIP, "AIS proxy/gateway IP address or hostname")
	f.StringVar(&port, "port", "8080", "AIS proxy/gateway port")

	//
	// s3 direct (NOTE: with no aistore in-between)
	//
	f.StringVar(&s3Endpoint, "s3endpoint", "", "S3 endpoint to read/write s3 bucket directly (with no aistore)")
	f.StringVar(&s3Profile, "s3profile", "", "Other then default S3 config profile referencing alternative credentials")

	DurationExtVar(f, &p.duration, "duration", time.Minute,
		"Benchmark duration (0 - run forever or until Ctrl-C). \n"+
			"If not specified and totalputsize > 0, aisloader runs until totalputsize reached. Otherwise aisloader runs until first of duration and "+
			"totalputsize reached")

	f.IntVar(&p.numWorkers, "numworkers", 10, "Number of goroutine workers operating on AIS in parallel")
	f.IntVar(&p.putPct, "pctput", 0, "Percentage of PUTs in the aisloader-generated workload")
	f.StringVar(&p.tmpDir, "tmpdir", "/tmp/ais", "Local directory to store temporary files")
	f.StringVar(&p.putSizeUpperBoundStr, "totalputsize", "0",
		"Stop PUT workload once cumulative PUT size reaches or exceeds this value (can contain standard multiplicative suffix K, MB, GiB, etc.; 0 - unlimited")
	BoolExtVar(f, &p.cleanUp, "cleanup", "true: remove bucket upon benchmark termination (must be specified for aistore buckets)")
	f.BoolVar(&p.verifyHash, "verifyhash", false,
		"true: checksum-validate GET: recompute object checksums and validate it against the one received with the GET metadata")

	f.StringVar(&p.minSizeStr, "minsize", "", "Minimum object size (with or without multiplicative suffix K, MB, GiB, etc.)")
	f.StringVar(&p.maxSizeStr, "maxsize", "", "Maximum object size (with or without multiplicative suffix K, MB, GiB, etc.)")
	f.StringVar(&p.readerType, "readertype", readers.TypeSG,
		fmt.Sprintf("Type of reader: %s(default) | %s | %s | %s", readers.TypeSG, readers.TypeFile, readers.TypeRand, readers.TypeTar))
	f.StringVar(&p.loaderID, "loaderid", "0", "ID to identify a loader among multiple concurrent instances")
	f.StringVar(&p.statsdIP, "statsdip", "localhost", "StatsD IP address or hostname")
	f.StringVar(&p.tokenFile, "tokenfile", "", "authentication token (FQN)") // see also: AIS_AUTHN_TOKEN_FILE
	f.IntVar(&p.statsdPort, "statsdport", 8125, "StatsD UDP port")
	f.BoolVar(&p.statsdProbe, "test-probe StatsD server prior to benchmarks", false, "when enabled probes StatsD server prior to running")
	f.IntVar(&p.batchSize, "batchsize", 100, "Batch size to list and delete")
	f.StringVar(&p.bPropsStr, "bprops", "", "JSON string formatted as per the SetBucketProps API and containing bucket properties to apply")
	f.Int64Var(&p.seed, "seed", 0, "Random seed to achieve deterministic reproducible results (0 - use current time in nanoseconds)")
	f.BoolVar(&p.jsonFormat, "json", false, "Defines whether to print output in JSON format")
	f.StringVar(&p.readOffStr, "readoff", "", "Read range offset (can contain multiplicative suffix K, MB, GiB, etc.)")
	f.StringVar(&p.readLenStr, "readlen", "", "Read range length (can contain multiplicative suffix; 0 - GET full object)")
	f.Uint64Var(&p.maxputs, "maxputs", 0, "Maximum number of objects to PUT")
	f.UintVar(&p.numEpochs, "epochs", 0, "Number of \"epochs\" to run whereby each epoch entails full pass through the entire listed bucket")
	f.BoolVar(&p.skipList, "skiplist", false, "Whether to skip listing objects in a bucket before running 100% PUT workload")
	f.StringVar(&p.fileList, "filelist", "", "Local or locally accessible text file file containing object names (for subsequent reading)")

	//
	// object naming
	//
	f.Uint64Var(&p.loaderCnt, "loadernum", 0,
		"total number of aisloaders running concurrently and generating combined load. If defined, must be greater than the loaderid and cannot be used together with loaderidhashlen")
	f.BoolVar(&p.getLoaderID, "getloaderid", false,
		"true: print stored/computed unique loaderID aka aisloader identifier and exit")
	f.UintVar(&p.loaderIDHashLen, "loaderidhashlen", 0,
		"Size (in bits) of the generated aisloader identifier. Cannot be used together with loadernum")
	f.BoolVar(&p.randomObjName, "randomname", true,
		"true: generate object names of 32 random characters. This option is ignored when loadernum is defined")
	f.BoolVar(&p.randomProxy, "randomproxy", false,
		"true: select random gateway (\"proxy\") to execute I/O request")
	f.StringVar(&p.subDir, "subdir", "", "Virtual destination directory for all aisloader-generated objects")
	f.Uint64Var(&p.putShards, "putshards", 0, "Spread generated objects over this many subdirectories (max 100k)")
	f.BoolVar(&p.uniqueGETs, "uniquegets", true,
		"true: GET objects randomly and equally. Meaning, make sure *not* to GET some objects more frequently than the others")

	//
	// advanced usage
	//
	f.BoolVar(&p.getConfig, "getconfig", false,
		"true: generate control plane load by reading AIS proxy configuration (that is, instead of reading/writing data exercise control path)")
	f.StringVar(&p.statsOutput, "stats-output", "", "filename to log statistics (empty string translates as standard output (default))")
	f.BoolVar(&p.stoppable, "stoppable", false, "true: stop upon CTRL-C")
	f.BoolVar(&p.dryRun, "dry-run", false, "true: show the configuration and parameters that aisloader will use for benchmark")
	f.BoolVar(&p.traceHTTP, "trace-http", false, "true: trace HTTP latencies") // see httpLatencies
	f.StringVar(&p.cksumType, "cksum-type", cos.ChecksumXXHash, "cksum type to use for put object requests")

	// ETL
	f.StringVar(&p.etlName, "etl", "", "name of an ETL applied to each object on GET request. One of '', 'tar2tf', 'md5', 'echo'")
	f.StringVar(&p.etlSpecPath, "etl-spec", "", "path to an ETL spec to be applied to each object on GET request.")

	// temp replace flags.Usage callback:
	// too many flags with actual parsing error quickly disappearing from view
	orig := f.Usage
	f.Usage = func() {
		fmt.Println("Run `aisloader` or `aisloader version`, or see 'docs/aisloader.md' for details and usage examples.")
	}
	f.Parse(os.Args[1:])
	f.Usage = orig

	if len(os.Args[1:]) == 0 {
		printUsage(f)
		os.Exit(0)
	}

	os.Args = []string{os.Args[0]}
	flag.Parse() // Called so that imported packages don't complain

	if flagUsage || (f.NArg() != 0 && f.Arg(0) == "usage") {
		printUsage(f)
		os.Exit(0)
	}
	if flagVersion || (f.NArg() != 0 && f.Arg(0) == "version") {
		fmt.Printf("version %s (build %s)\n", _version, _buildtime)
		os.Exit(0)
	}
}

// validate command line and finish initialization
func _init(p *params) (err error) {
	if p.bck.Name != "" {
		if p.cleanUp.Val && isDirectS3() {
			return errors.New("direct S3 access via '-s3endpoint': option '-cleanup' is not supported yet")
		}
		if !p.cleanUp.IsSet && !isDirectS3() {
			fmt.Println("\nNote: `-cleanup` is a required option. Beware! When -cleanup=true the bucket will be destroyed upon completion of the benchmark.")
			fmt.Println("      The option must be specified in the command line, e.g.: `--cleanup=false`")
			os.Exit(1)
		}
	}

	if p.seed == 0 {
		p.seed = mono.NanoTime()
	}
	rnd = rand.New(rand.NewSource(p.seed))

	if p.putSizeUpperBoundStr != "" {
		if p.putSizeUpperBound, err = cos.ParseSize(p.putSizeUpperBoundStr, cos.UnitsIEC); err != nil {
			return fmt.Errorf("failed to parse total PUT size %s: %v", p.putSizeUpperBoundStr, err)
		}
	}

	if p.minSizeStr != "" {
		if p.minSize, err = cos.ParseSize(p.minSizeStr, cos.UnitsIEC); err != nil {
			return fmt.Errorf("failed to parse min size %s: %v", p.minSizeStr, err)
		}
	} else {
		p.minSize = cos.MiB
	}

	if p.maxSizeStr != "" {
		if p.maxSize, err = cos.ParseSize(p.maxSizeStr, cos.UnitsIEC); err != nil {
			return fmt.Errorf("failed to parse max size %s: %v", p.maxSizeStr, err)
		}
	} else {
		p.maxSize = cos.GiB
	}

	if !p.duration.IsSet {
		if p.putSizeUpperBound != 0 || p.numEpochs != 0 {
			// user specified putSizeUpperBound or numEpochs, but not duration, override default 1 minute
			// and run aisloader until other threshold is reached
			p.duration.Val = time.Duration(math.MaxInt64)
		} else {
			fmt.Printf("\nDuration not specified - running for %v\n\n", p.duration.Val)
		}
	}

	// Sanity check
	if p.maxSize < p.minSize {
		return fmt.Errorf("invalid option: min and max size (%d, %d), respectively", p.minSize, p.maxSize)
	}

	if p.putPct < 0 || p.putPct > 100 {
		return fmt.Errorf("invalid option: PUT percent %d", p.putPct)
	}

	if p.skipList {
		if p.fileList != "" {
			fmt.Printf("Warning: '-skiplist' is redundant (implied) when '-filelist' is specified")
		} else if p.putPct != 100 {
			return errors.New("invalid option: '-skiplist' is only valid for 100% PUT workloads")
		}
	}

	// direct s3 access vs other command line
	if isDirectS3() {
		if p.randomProxy {
			return errors.New("command line options '-s3endpoint' and '-randomproxy' are mutually exclusive")
		}
		if ip != "" && ip != defaultClusterIP && ip != defaultClusterIPv4 {
			return errors.New("command line options '-s3endpoint' and '-ip' are mutually exclusive")
		}
		if port != "" && port != "8080" { // TODO: ditto
			return errors.New("command line options '-s3endpoint' and '-port' are mutually exclusive")
		}
		if p.traceHTTP {
			return errors.New("direct S3 access via '-s3endpoint': HTTP tracing is not supported yet")
		}
		if p.cleanUp.Val {
			return errors.New("direct S3 access via '-s3endpoint': '-cleanup' option is not supported yet")
		}
		if p.verifyHash {
			return errors.New("direct S3 access via '-s3endpoint': '-verifyhash' option is not supported yet")
		}
		if p.readOffStr != "" || p.readLenStr != "" {
			return errors.New("direct S3 access via '-s3endpoint': Read range is not supported yet")
		}
	}

	if p.statsShowInterval < 0 {
		return fmt.Errorf("invalid option: stats show interval %d", p.statsShowInterval)
	}

	if p.readOffStr != "" {
		if p.readOff, err = cos.ParseSize(p.readOffStr, cos.UnitsIEC); err != nil {
			return fmt.Errorf("failed to parse read offset %s: %v", p.readOffStr, err)
		}
	}
	if p.readLenStr != "" {
		if p.readLen, err = cos.ParseSize(p.readLenStr, cos.UnitsIEC); err != nil {
			return fmt.Errorf("failed to parse read length %s: %v", p.readLenStr, err)
		}
	}

	if p.loaderID == "" {
		return fmt.Errorf("loaderID can't be empty")
	}

	loaderID, parseErr := strconv.ParseUint(p.loaderID, 10, 64)
	if p.loaderCnt == 0 && p.loaderIDHashLen == 0 {
		if p.randomObjName {
			useRandomObjName = true
			if parseErr != nil {
				return errors.New("loaderID as string only allowed when using loaderIDHashLen")
			}
			// don't have to set suffixIDLen as userRandomObjName = true
			suffixID = loaderID
		} else {
			// stats will be using loaderID
			// but as suffixIDMaskLen = 0, object names will be just consecutive numbers
			suffixID = loaderID
			suffixIDMaskLen = 0
		}
	} else {
		if p.loaderCnt > 0 && p.loaderIDHashLen > 0 {
			return fmt.Errorf("loadernum and loaderIDHashLen can't be > 0 at the same time")
		}

		if p.loaderIDHashLen > 0 {
			if p.loaderIDHashLen == 0 || p.loaderIDHashLen > 63 {
				return fmt.Errorf("loaderIDHashLen has to be larger than 0 and smaller than 64")
			}

			suffixIDMaskLen = cos.CeilAlign(p.loaderIDHashLen, 4)
			suffixID = getIDFromString(p.loaderID, suffixIDMaskLen)
		} else {
			// p.loaderCnt > 0
			if parseErr != nil {
				return fmt.Errorf("loadername has to be a number when using loadernum")
			}
			if loaderID > p.loaderCnt {
				return fmt.Errorf("loaderid has to be smaller than loadernum")
			}

			suffixIDMaskLen = loaderMaskFromTotalLoaders(p.loaderCnt)
			suffixID = loaderID
		}
	}

	if p.subDir != "" {
		p.subDir = filepath.Clean(p.subDir)
		if p.subDir[0] == '/' {
			return fmt.Errorf("object name prefix can't start with /")
		}
	}

	if p.putShards > 100000 {
		return fmt.Errorf("putshards should not exceed 100000")
	}

	if err := cos.ValidateCksumType(p.cksumType); err != nil {
		return err
	}

	if p.etlName != "" && p.etlSpecPath != "" {
		return fmt.Errorf("etl and etl-spec flag can't be set both")
	}

	if p.etlSpecPath != "" {
		fh, err := os.Open(p.etlSpecPath)
		if err != nil {
			return err
		}
		etlSpec, err := io.ReadAll(fh)
		fh.Close()
		if err != nil {
			return err
		}
		etlInitSpec, err = tetl.SpecToInitMsg(etlSpec)
		if err != nil {
			return err
		}
	}

	if p.etlName != "" {
		etlSpec, err := tetl.GetTransformYaml(p.etlName)
		if err != nil {
			return err
		}
		etlInitSpec, err = tetl.SpecToInitMsg(etlSpec)
		if err != nil {
			return err
		}
	}

	if p.bPropsStr != "" {
		var bprops cmn.Bprops
		jsonStr := strings.TrimRight(p.bPropsStr, ",")
		if !strings.HasPrefix(jsonStr, "{") {
			jsonStr = "{" + strings.TrimRight(jsonStr, ",") + "}"
		}

		if err := jsoniter.Unmarshal([]byte(jsonStr), &bprops); err != nil {
			return fmt.Errorf("failed to parse bucket properties: %v", err)
		}

		p.bProps = bprops
		if p.bProps.EC.Enabled {
			// fill EC defaults
			if p.bProps.EC.ParitySlices == 0 {
				p.bProps.EC.ParitySlices = 1
			}
			if p.bProps.EC.DataSlices == 0 {
				p.bProps.EC.DataSlices = 1
			}

			if p.bProps.EC.ParitySlices < 1 || p.bProps.EC.ParitySlices > 32 {
				return fmt.Errorf(
					"invalid number of parity slices: %d, it must be between 1 and 32",
					p.bProps.EC.ParitySlices)
			}
			if p.bProps.EC.DataSlices < 1 || p.bProps.EC.DataSlices > 32 {
				return fmt.Errorf(
					"invalid number of data slices: %d, it must be between 1 and 32",
					p.bProps.EC.DataSlices)
			}
		}

		if p.bProps.Mirror.Enabled {
			// fill mirror default properties
			if p.bProps.Mirror.Burst == 0 {
				p.bProps.Mirror.Burst = 512
			}
			if p.bProps.Mirror.Copies == 0 {
				p.bProps.Mirror.Copies = 2
			}
			if p.bProps.Mirror.Copies != 2 {
				return fmt.Errorf(
					"invalid number of mirror copies: %d, it must equal 2",
					p.bProps.Mirror.Copies)
			}
		}
	}

	var useHTTPS bool
	if !isDirectS3() {
		// AIS endpoint: http://ip:port _or_ AIS_ENDPOINT env
		aisEndpoint := "http://" + ip + ":" + port

		// see also: tlsArgs
		envEndpoint = os.Getenv(env.AIS.Endpoint)
		if envEndpoint != "" {
			if ip != "" && ip != defaultClusterIP && ip != defaultClusterIPv4 {
				return fmt.Errorf("'%s=%s' environment and '--ip=%s' command-line are mutually exclusive",
					env.AIS.Endpoint, envEndpoint, ip)
			}
			aisEndpoint = envEndpoint
		}

		traceHTTPSig.Store(p.traceHTTP)

		scheme, address := cmn.ParseURLScheme(aisEndpoint)
		if scheme == "" {
			scheme = "http"
		}
		if scheme != "http" && scheme != "https" {
			return fmt.Errorf("invalid aistore endpoint %q: unknown URI scheme %q", aisEndpoint, scheme)
		}

		// TODO: validate against cluster map (see api.GetClusterMap below)
		p.proxyURL = scheme + "://" + address
		useHTTPS = scheme == "https"
	}

	p.bp = api.BaseParams{URL: p.proxyURL}
	if useHTTPS {
		// environment to override client config
		cmn.EnvToTLS(&sargs)
		p.bp.Client = cmn.NewClientTLS(cargs, sargs)
	} else {
		p.bp.Client = cmn.NewClient(cargs)
	}

	// NOTE: auth token is assigned below when we execute the very first API call
	return nil
}

func isDirectS3() bool {
	debug.Assert(flag.Parsed())
	return s3Endpoint != ""
}

func loaderMaskFromTotalLoaders(totalLoaders uint64) uint {
	// take first bigger power of 2, then take first bigger or equal number
	// divisible by 4. This makes loaderID more visible in hex object name
	return cos.CeilAlign(cos.FastLog2Ceil(totalLoaders), 4)
}

func printArguments(set *flag.FlagSet) {
	w := tabwriter.NewWriter(os.Stdout, 0, 8, 1, '\t', 0)

	fmt.Fprintf(w, "==== COMMAND LINE ARGUMENTS ====\n")
	fmt.Fprintf(w, "=========== DEFAULTS ===========\n")
	set.VisitAll(func(f *flag.Flag) {
		if f.Value.String() == f.DefValue {
			_, _ = fmt.Fprintf(w, "%s:\t%s\n", f.Name, f.Value.String())
		}
	})
	fmt.Fprintf(w, "============ CUSTOM ============\n")
	set.VisitAll(func(f *flag.Flag) {
		if f.Value.String() != f.DefValue {
			_, _ = fmt.Fprintf(w, "%s:\t%s\n", f.Name, f.Value.String())
		}
	})
	fmt.Fprintf(w, "HTTP trace:\t%v\n", runParams.traceHTTP)
	fmt.Fprintf(w, "=================================\n\n")
	w.Flush()
}

// newStats returns a new stats object with given time as the starting point
func newStats(t time.Time) sts {
	return sts{
		put:       stats.NewHTTPReq(t),
		get:       stats.NewHTTPReq(t),
		getConfig: stats.NewHTTPReq(t),
		statsd:    stats.NewStatsdMetrics(t),
	}
}

// aggregate adds another sts to self
func (s *sts) aggregate(other *sts) {
	s.get.Aggregate(other.get)
	s.put.Aggregate(other.put)
	s.getConfig.Aggregate(other.getConfig)
}

func setupBucket(runParams *params, created *bool) error {
	if runParams.getConfig {
		return nil
	}
	if strings.Contains(runParams.bck.Name, apc.BckProviderSeparator) {
		bck, objName, err := cmn.ParseBckObjectURI(runParams.bck.Name, cmn.ParseURIOpts{})
		if err != nil {
			return err
		}
		if objName != "" {
			return fmt.Errorf("expecting bucket name or a bucket URI with no object name in it: %s => [%v, %s]",
				runParams.bck, bck, objName)
		}
		if runParams.bck.Provider != apc.AIS /*cmdline default*/ && runParams.bck.Provider != bck.Provider {
			return fmt.Errorf("redundant and different bucket provider: %q vs %q in %s",
				runParams.bck.Provider, bck.Provider, bck)
		}
		runParams.bck = bck
	}
	if isDirectS3() && apc.ToScheme(runParams.bck.Provider) != apc.S3Scheme {
		return fmt.Errorf("option --s3endpoint requires s3 bucket (have %s)", runParams.bck)
	}
	if runParams.bck.Provider != apc.AIS {
		return nil
	}

	//
	// ais:// or ais://@remais
	//
	if runParams.bck.Name == "" {
		runParams.bck.Name = cos.CryptoRandS(8)
		fmt.Printf("New bucket name %q\n", runParams.bck.Name)
	}
	exists, err := api.QueryBuckets(runParams.bp, cmn.QueryBcks(runParams.bck), apc.FltPresent)
	if err != nil {
		return fmt.Errorf("%s not found: %v", runParams.bck, err)
	}
	if !exists {
		if err := api.CreateBucket(runParams.bp, runParams.bck, nil); err != nil {
			return fmt.Errorf("failed to create %s: %v", runParams.bck, err)
		}
		*created = true
	}
	if runParams.bPropsStr == "" {
		return nil
	}
	propsToUpdate := cmn.BpropsToSet{}
	// update bucket props if bPropsStr is set
	oldProps, err := api.HeadBucket(runParams.bp, runParams.bck, true /* don't add */)
	if err != nil {
		return fmt.Errorf("failed to read bucket %s properties: %v", runParams.bck, err)
	}
	change := false
	if runParams.bProps.EC.Enabled != oldProps.EC.Enabled {
		propsToUpdate.EC = &cmn.ECConfToSet{
			Enabled:      apc.Bool(runParams.bProps.EC.Enabled),
			ObjSizeLimit: apc.Int64(runParams.bProps.EC.ObjSizeLimit),
			DataSlices:   apc.Int(runParams.bProps.EC.DataSlices),
			ParitySlices: apc.Int(runParams.bProps.EC.ParitySlices),
		}
		change = true
	}
	if runParams.bProps.Mirror.Enabled != oldProps.Mirror.Enabled {
		propsToUpdate.Mirror = &cmn.MirrorConfToSet{
			Enabled: apc.Bool(runParams.bProps.Mirror.Enabled),
			Copies:  apc.Int64(runParams.bProps.Mirror.Copies),
			Burst:   apc.Int(runParams.bProps.Mirror.Burst),
		}
		change = true
	}
	if change {
		if _, err = api.SetBucketProps(runParams.bp, runParams.bck, &propsToUpdate); err != nil {
			return fmt.Errorf("failed to enable EC for the bucket %s properties: %v", runParams.bck, err)
		}
	}
	return nil
}

func getIDFromString(val string, hashLen uint) uint64 {
	hash := xxhash.Checksum64S(cos.UnsafeB(val), cos.MLCG32)
	// keep just the loaderIDHashLen bytes
	hash <<= 64 - hashLen
	hash >>= 64 - hashLen
	return hash
}

func sendStatsdStats(s *sts) {
	s.statsd.SendAll(statsdC)
}

func cleanup() {
	stopping.Store(true)
	time.Sleep(time.Second)
	fmt.Println(now() + " Cleaning up...")
	if bucketObjsNames != nil {
		// `bucketObjsNames` has been actually assigned to/initialized.
		var (
			w       = runParams.numWorkers
			objsLen = bucketObjsNames.Len()
			n       = objsLen / w
			wg      = &sync.WaitGroup{}
		)
		for i := 0; i < w; i++ {
			wg.Add(1)
			go cleanupObjs(bucketObjsNames.Names()[i*n:(i+1)*n], wg)
		}
		if objsLen%w != 0 {
			wg.Add(1)
			go cleanupObjs(bucketObjsNames.Names()[n*w:], wg)
		}
		wg.Wait()
	}

	if runParams.bck.IsAIS() {
		api.DestroyBucket(runParams.bp, runParams.bck)
	}
	fmt.Println(now() + " Done")
}

func cleanupObjs(objs []string, wg *sync.WaitGroup) {
	defer wg.Done()

	t := len(objs)
	if t == 0 {
		return
	}

	// Only delete objects if it's not an AIS bucket (because otherwise we just go ahead
	// and remove the bucket itself)
	if !runParams.bck.IsAIS() {
		b := min(t, runParams.batchSize)
		n := t / b
		for i := 0; i < n; i++ {
			xid, err := api.DeleteMultiObj(runParams.bp, runParams.bck, objs[i*b:(i+1)*b], "" /*template*/)
			if err != nil {
				fmt.Println("delete err ", err)
			}
			args := xact.ArgsMsg{ID: xid, Kind: apc.ActDeleteObjects}
			if _, err = api.WaitForXactionIC(runParams.bp, &args); err != nil {
				fmt.Println("wait for xaction err ", err)
			}
		}

		if t%b != 0 {
			xid, err := api.DeleteMultiObj(runParams.bp, runParams.bck, objs[n*b:], "" /*template*/)
			if err != nil {
				fmt.Println("delete err ", err)
			}
			args := xact.ArgsMsg{ID: xid, Kind: apc.ActDeleteObjects}
			if _, err = api.WaitForXactionIC(runParams.bp, &args); err != nil {
				fmt.Println("wait for xaction err ", err)
			}
		}
	}

	if runParams.readerType == readers.TypeFile {
		for _, obj := range objs {
			if err := os.Remove(runParams.tmpDir + "/" + obj); err != nil {
				fmt.Println("delete local file err ", err)
			}
		}
	}
}

func objNamesFromFile() (names []string, err error) {
	var fh *os.File
	if fh, err = os.Open(runParams.fileList); err != nil {
		return
	}
	names = make([]string, 0, 1024)
	scanner := bufio.NewScanner(fh)
	regex := regexp.MustCompile(`\s`)
	for scanner.Scan() {
		n := strings.TrimSpace(scanner.Text())
		if strings.Contains(n, " ") || regex.MatchString(n) {
			continue
		}
		names = append(names, n)
	}
	fh.Close()
	return
}

func listObjects() error {
	var (
		names []string
		err   error
	)
	switch {
	case runParams.fileList != "":
		names, err = objNamesFromFile()
	case isDirectS3():
		names, err = s3ListObjects()
	default:
		names, err = listObjectNames(runParams.bp, runParams.bck, runParams.subDir)
	}
	if err != nil {
		return err
	}

	if !runParams.uniqueGETs {
		bucketObjsNames = &namegetter.RandomNameGetter{}
	} else {
		bucketObjsNames = &namegetter.RandomUniqueNameGetter{}

		// Permutation strategies seem to be always better (they use more memory though)
		if runParams.putPct == 0 {
			bucketObjsNames = &namegetter.PermutationUniqueNameGetter{}

			// Number from benchmarks: aisloader/tests/objnamegetter_test.go
			// After 50k overhead on new goroutine and WaitGroup becomes smaller than benefits
			if len(names) > 50000 {
				bucketObjsNames = &namegetter.PermutationUniqueImprovedNameGetter{}
			}
		}
	}
	bucketObjsNames.Init(names, rnd)
	return err
}
