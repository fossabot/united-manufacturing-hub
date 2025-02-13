package main

/*
Important principles: stateless as much as possible
*/

/*
Target architecture:

Incoming REST call --> helpers.go
There is one function for that specific call. It parses the parameters and executes further functions:
1. One or multiple function getting the data from the database (database.go)
2. Only one function processing everything. In this function no database calls are allowed to be as stateless as possible (dataprocessing.go)
Then the results are bundled together and a return JSON is created.
*/

import (
	"fmt"
	"github.com/gin-contrib/gzip"
	ginzap "github.com/gin-contrib/zap"
	"github.com/united-manufacturing-hub/umh-utils/logger"
	"github.com/united-manufacturing-hub/united-manufacturing-hub/cmd/factoryinsight/database"
	"github.com/united-manufacturing-hub/united-manufacturing-hub/cmd/factoryinsight/helpers"
	apiV1 "github.com/united-manufacturing-hub/united-manufacturing-hub/cmd/factoryinsight/v1"
	v2controllers "github.com/united-manufacturing-hub/united-manufacturing-hub/cmd/factoryinsight/v2/controllers"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/heptiolabs/healthcheck"
	"github.com/united-manufacturing-hub/united-manufacturing-hub/internal"
	"go.uber.org/zap"
)

var (
	buildtime       string
	shutdownEnabled bool
)

func main() {
	// Initialize zap logging
	log := logger.New("LOGGING_LEVEL")
	defer func(logger *zap.SugaredLogger) {
		err := logger.Sync()
		if err != nil {
			panic(err)
		}
	}(log)
	zap.S().Infof("This is factoryinsight build date: %s", buildtime)

	internal.Initfgtrace()

	PQHost := "db"
	// Read environment variables
	if os.Getenv("POSTGRES_HOST") != "" {
		PQHost = os.Getenv("POSTGRES_HOST")
	} else {
		zap.S().Infof("Postgres_Host [POSTGRES_HOST] not set, using default (%s)", PQHost)
	}

	// Read port and convert to integer
	PQPortString := "5432"
	if os.Getenv("POSTGRES_PORT") != "" {
		PQPortString = os.Getenv("POSTGRES_PORT")
	} else {
		zap.S().Infof("Postgres_Port [POSTGRES_PORT] not set, using default(%s)", PQPortString)
	}
	PQPort, err := strconv.Atoi(PQPortString)
	if err != nil {
		zap.S().Errorf("Cannot parse POSTGRES_PORT: not a number", PQPortString)
		return // Abort program
	}

	// Read in other environment variables
	PQUser, PQUserEnvSet := os.LookupEnv("POSTGRES_USER")
	if !PQUserEnvSet {
		zap.S().Fatal("PQ User (PQ_USER) must be set")
	}
	PQPassword, PQPasswordEnvSet := os.LookupEnv("POSTGRES_PASSWORD")
	if !PQPasswordEnvSet {
		zap.S().Fatal("PQ Password (PQ_PASSWORD) must be set")
	}
	PWDBName, PWDBNameEnvSet := os.LookupEnv("POSTGRES_DATABASE")
	if !PWDBNameEnvSet {
		zap.S().Fatal("PWDB Name (PWDB_NAME) must be set")
	}

	// Loading up user accounts
	accounts := gin.Accounts{}

	zap.S().Debugf("Loading accounts from environment..")

	for i := 1; i <= 100; i++ {
		tempUser, tempUserEnvSet := os.LookupEnv("CUSTOMER_NAME_" + strconv.Itoa(i))
		tempPassword, tempPasswordEnvSet := os.LookupEnv("CUSTOMER_PASSWORD_" + strconv.Itoa(i))
		if tempUserEnvSet && tempPasswordEnvSet {
			zap.S().Infof("Added account for " + tempUser)
			accounts[tempUser] = tempPassword
		}
	}

	// also add admin access
	RESTUser, RESTUserEnvSet := os.LookupEnv("FACTORYINSIGHT_USER")
	if !RESTUserEnvSet {
		zap.S().Fatal("Rest User (REST_USER) must be set")
	}
	RESTPassword, RESTPasswordEnvSet := os.LookupEnv("FACTORYINSIGHT_PASSWORD")
	if !RESTPasswordEnvSet {
		zap.S().Fatal("REST Password (REST_PASSWORD) must be set")
	}
	accounts[RESTUser] = RESTPassword

	// get currentVersion
	version, versionEnvSet := os.LookupEnv("VERSION")
	if !versionEnvSet {
		zap.S().Fatal("Version (VERSION) must be set")
	}
	// parse as int
	currentVersion, err := strconv.Atoi(version)
	if err != nil {
		zap.S().Fatalf("Cannot parse VERSION: not a number (%s)", version)
		return // Abort program
	}

	zap.S().Debugf("Starting program..")

	redisURI, redisURIEnvSet := os.LookupEnv("REDIS_URI")
	if !redisURIEnvSet {
		zap.S().Fatal("RedisURI (REDIS_URI) must be set")
	}
	redisURI2, redisURI2EnvSet := os.LookupEnv("REDIS_URI2")
	if !redisURI2EnvSet {
		zap.S().Fatal("redisURI2 (REDIS_URI2) must be set")
	}
	redisURI3, redisURI3EnvSet := os.LookupEnv("REDIS_URI3")
	if !redisURI3EnvSet {
		zap.S().Fatal("RedisURI3 (REDIS_URI3) must be set")
	}
	redisPassword, redisPasswordEnvSet := os.LookupEnv("REDIS_PASSWORD")
	if !redisPasswordEnvSet {
		zap.S().Fatal("RedisPassword (REDIS_PASSWORD) must be set")
	}
	redisDB := 0 // default database

	dryRun, dryRunEnvSet := os.LookupEnv("DRY_RUN")
	if !dryRunEnvSet {
		dryRun = "false"
	}
	internal.InitCache(redisURI, redisURI2, redisURI3, redisPassword, redisDB, dryRun)

	zap.S().Debugf("Cache initialized..", redisURI)

	health := healthcheck.NewHandler()
	shutdownEnabled = false
	health.AddLivenessCheck("goroutine-threshold", healthcheck.GoroutineCountCheck(100))
	health.AddReadinessCheck("shutdownEnabled", isShutdownEnabled())
	go func() {
		/* #nosec G114 */
		errX := http.ListenAndServe("0.0.0.0:8086", health)
		if errX != nil {
			zap.S().Errorf("Error starting healthcheck: %s", errX)
		}
	}()

	zap.S().Debugf("Healthcheck initialized..", redisURI)

	sigs := make(chan os.Signal, 1)
	database.Connect(PQUser, PQPassword, PWDBName, PQHost, PQPort, sigs)

	zap.S().Debugf("DB initialized..", PQHost)

	insecureNoAuthString, insecureNotAuthSet := os.LookupEnv("INSECURE_NO_AUTH")
	if insecureNotAuthSet {
		helpers.InsecureNoAuth, err = strconv.ParseBool(insecureNoAuthString)
		if err != nil {
			zap.S().Errorf("Cannot parse INSECURE_NO_AUTH: %s (%s)", err, insecureNoAuthString)
		}
		if helpers.InsecureNoAuth {
			for i := 0; i < 50; i++ {
				zap.S().Warnf("INSECURE_NO_AUTH is set to true. This is a security risk. Do not use in production.")
			}
			zap.S().Warnf("Sleeping for 10 seconds to allow you to cancel the program.")
			time.Sleep(10 * time.Second)
		}
	}

	setupRestAPI(accounts, currentVersion)

	// Allow graceful shutdown
	signal.Notify(sigs, syscall.SIGTERM)

	go func() {
		// before you trapped SIGTERM your process would
		// have exited, so we are now on borrowed time.
		//
		// Kubernetes sends SIGTERM 30 seconds before
		// shutting down the pod.

		sig := <-sigs

		// Log the received signal
		zap.S().Infof("Received SIGTERM", sig)

		// ... close TCP connections here.
		ShutdownApplicationGraceful()

	}()

	select {} // block forever
}

func isShutdownEnabled() healthcheck.Check {
	return func() error {
		if shutdownEnabled {
			return fmt.Errorf("shutdown")
		}
		return nil
	}
}

// ShutdownApplicationGraceful shutsdown the entire application including MQTT and database
func ShutdownApplicationGraceful() {
	zap.S().Infof("Shutting down application")
	shutdownEnabled = true

	time.Sleep(4 * time.Minute) // Wait until all remaining open connections are handled

	database.Shutdown()

	zap.S().Infof("Successful shutdown. Exiting.")

	// Gracefully exit.
	// (Use runtime.GoExit() if you need to call defers)
	os.Exit(0)
}

// setupRestAPI initializes the REST API and starts listening
func setupRestAPI(accounts gin.Accounts, version int) {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()

	// Add a ginzap middleware, which:
	//   - Logs all requests, like a combined access and error log.
	//   - Logs to stdout.
	//   - RFC3339 with UTC time format.
	router.Use(ginzap.Ginzap(zap.L(), time.RFC3339, true))

	// Logs all panic to error log
	//   - stack means whether output the stack info.
	router.Use(ginzap.RecoveryWithZap(zap.L(), true))

	// Use gzip for all requests
	router.Use(gzip.Gzip(gzip.DefaultCompression))

	// Healthcheck
	router.GET(
		"/", func(c *gin.Context) {
			c.String(http.StatusOK, "online")
		})

	// Version of the API
	if version >= 1 {
		zap.S().Infof("Starting API version 1")
		v1 := router.Group("/api/v1", gin.BasicAuth(accounts))
		if helpers.InsecureNoAuth {
			v1 = router.Group("/api/v1")
		}
		{
			// WARNING: Need to check in each specific handler whether the user is actually allowed to access it, so that valid user "ia" cannot access data for customer "abc"
			v1.GET("/:customer", apiV1.GetLocationsHandler)
			v1.GET("/:customer/:location", apiV1.GetAssetsHandler)
			v1.GET("/:customer/:location/:asset", apiV1.GetValuesHandler)
			v1.GET("/:customer/:location/:asset/:value", apiV1.GetDataHandler)
		}
	}

	if version >= 2 {
		zap.S().Infof("Starting API version 2")
		v2 := router.Group("/api/v2", gin.BasicAuth(accounts))
		if helpers.InsecureNoAuth {
			v2 = router.Group("/api/v2")
		}
		{
			v2.GET("/treeStructure", v2controllers.GetTreeStructureHandler)
			v2.GET("/:enterpriseName", v2controllers.GetSitesHandler)
			v2.GET("/:enterpriseName/configuration", v2controllers.GetConfigurationHandler)
			v2.GET("/:enterpriseName/database-stats", v2controllers.GetDatabaseStatisticsHandler)
			v2.GET("/:enterpriseName/:siteName", v2controllers.GetAreasHandler)
			v2.GET("/:enterpriseName/:siteName/:areaName", v2controllers.GetProductionLinesHandler)
			v2.GET("/:enterpriseName/:siteName/:areaName/:productionLineName", v2controllers.GetWorkCellsHandler)
			v2.GET(
				"/:enterpriseName/:siteName/:areaName/:productionLineName/:workCellName",
				v2controllers.GetDataFormatHandler)
			v2.GET(
				"/:enterpriseName/:siteName/:areaName/:productionLineName/:workCellName/getValues",
				v2controllers.GetValueTree)
			v2.GET(
				"/:enterpriseName/:siteName/:areaName/:productionLineName/:workCellName/tags",
				v2controllers.GetTagGroupsHandler)
			v2.GET(
				"/:enterpriseName/:siteName/:areaName/:productionLineName/:workCellName/tags/:tagGroupName",
				v2controllers.GetTagsHandler)
			v2.GET(
				"/:enterpriseName/:siteName/:areaName/:productionLineName/:workCellName/tags/:tagGroupName/:tagName",
				v2controllers.GetTagsDataHandler)
			v2.GET(
				"/:enterpriseName/:siteName/:areaName/:productionLineName/:workCellName/kpis",
				v2controllers.GetKpisMethodsHandler)
			v2.GET(
				"/:enterpriseName/:siteName/:areaName/:productionLineName/:workCellName/kpis/:kpisMethod",
				v2controllers.GetKpisDataHandler)
			v2.GET(
				"/:enterpriseName/:siteName/:areaName/:productionLineName/:workCellName/tables",
				v2controllers.GetTableTypesHandler)
			v2.GET(
				"/:enterpriseName/:siteName/:areaName/:productionLineName/:workCellName/tables/:tableType",
				v2controllers.GetTableDataHandler)
		}
	}

	/*
		v3 := router.Group("/api/v3", gin.BasicAuth(accounts))
		if helpers.InsecureNoAuth {
			v2 = router.Group("/api/v3")
		}
		{
			// Get all sites for a given enterprise
			v3.GET("/:enterpriseName", v3controllers.GetSitesHandler)
			// Get all areas for a given site)
			v3.GET("/:enterpriseName/:siteName", v3controllers.GetAreasHandler)
			// Get all production lines for a given area
			v3.GET("/:enterpriseName/:siteName/:areaName", v3controllers.GetProductionLinesHandler)
			// Get all work cells for a given production line
			v3.GET("/:enterpriseName/:siteName/:areaName/:productionLineName", v3controllers.GetWorkCellsHandler)
			// Get all data format for a given work cell
			v3.GET(
				"/:enterpriseName/:siteName/:areaName/:productionLineName/:workCellName",
				v3controllers.GetDataFormatHandler)
			// Get all tag groups for a given work cell
			v3.GET(
				"/:enterpriseName/:siteName/:areaName/:productionLineName/:workCellName/tags",
				v3controllers.GetTagGroupsHandler)
			// Get all tags for a given tag group
			v3.GET(
				"/:enterpriseName/:siteName/:areaName/:productionLineName/:workCellName/tags/:tagGroupName",
				v3controllers.GetTagsHandler)
			// Get specific data for a give work cell
			v3.GET(
				"/:enterpriseName/:siteName/:areaName/:productionLineName/:workCellName/tags/:tagGroupName/:tagName",
				v3controllers.GetTagsDataHandler)
			// Get KPIs methods for a given work cell
			v3.GET(
				"/:enterpriseName/:siteName/:areaName/:productionLineName/:workCellName/kpis",
				v3controllers.GetKpisMethodsHandler)
			// Get specific KPI data for a given work cell
			v3.GET(
				"/:enterpriseName/:siteName/:areaName/:productionLineName/:workCellName/kpis/:kpisMethod",
				v3controllers.GetKpisDataHandler)
			// Get tables types for a given work cell
			v3.GET(
				"/:enterpriseName/:siteName/:areaName/:productionLineName/:workCellName/tables",
				v3controllers.GetTableTypesHandler)
			// Get specific table data for a given work cell
			v3.GET(
				"/:enterpriseName/:siteName/:areaName/:productionLineName/:workCellName/tables/:tableType",
				v3controllers.GetTableDataHandler)

		}

	*/
	//dataFormat
	err := router.Run(":80")
	if err != nil {
		zap.S().Fatalf("Error starting the server: %s", err)
	}
}
