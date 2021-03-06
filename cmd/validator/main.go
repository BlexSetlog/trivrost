package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"

	"github.com/setlog/trivrost/pkg/misc"

	"github.com/setlog/trivrost/pkg/launcher/config"
	"github.com/setlog/trivrost/pkg/system"
)

func main() {
	flags := parseFlags()
	if flags.ActAsService {
		registerMetrics()
		actAsService(flags)
	} else {
		log.SetFlags(0)
		reps := validateDeploymentConfig(flags.DeploymentConfigUrl, flags.SkipUrlCheck, flags.SkipJarChek)
		logReports(reps, false)
		if reps.HaveError() {
			os.Exit(1)
		}
	}
}

func logReports(reps reports, errorsOnly bool) {
	for _, rep := range reps {
		if rep.isError {
			log.Printf("\033[0;91m%s\033[0m\n", rep.message)
		} else if !errorsOnly {
			log.Printf("%s\n", rep.message)
		}
	}
}

func validateDeploymentConfig(url string, skipUrlCheck bool, skipJarCheck bool) reports {
	expandedDeploymentConfig, err := getFile(url)
	if err != nil {
		return []*report{errorReport("Could not retrieve deployment-config from URL %s: %v.", url, err)}
	}

	err = config.ValidateDeploymentConfig(string(expandedDeploymentConfig))
	if err != nil {
		return []*report{errorReport("Could not validate deployment-config at URL %s: %v.", url, err)}
	}

	if !skipUrlCheck {
		return checkURLs(expandedDeploymentConfig, skipJarCheck)
	}
	return nil
}

func checkURLs(expandedDeploymentConfig []byte, skipJarCheck bool) reports {
	urlMap, reports := collectURLs(expandedDeploymentConfig, skipJarCheck)

	waitGroup := &sync.WaitGroup{}
	waitGroup.Add(len(urlMap))

	// Check all URLs in parallel.
	reportChan := make(chan *report, len(urlMap))
	for url, details := range urlMap {
		go checkURL(url, details, waitGroup, reportChan)
	}
	waitGroup.Wait()
	close(reportChan)

	for report := range reportChan {
		reports = append(reports, report)
	}
	return reports
}

func checkURL(url string, details checkDetails, waitGroup *sync.WaitGroup, reportChan chan *report) {
	defer waitGroup.Done()
	code, err := getHttpHeadResult(url)
	if err != nil {
		reportChan <- errorReport("HTTP HEAD request to URL '%s' failed: %v. (Check reason: %v)", url, err, details)
	} else if code != http.StatusOK {
		reportChan <- errorReport("HTTP HEAD request to URL '%s' yielded bad response code %d. (Check reason: %v)", url, code, details)
	} else {
		reportChan <- statusReport("OK: Resource %s is available. (Reason for check: %v)", url, details)
	}
}

func collectURLs(data []byte, skipJarCheck bool) (urlMap map[string]checkDetails, reps reports) {
	urlMap = make(map[string]checkDetails)
	for _, operatingsystem := range []string{"windows", "darwin", "linux"} {
		for _, arch := range []string{"386", "amd64"} {
			deploymentConfig := config.ParseDeploymentConfig(strings.NewReader(string(data)), operatingsystem, arch)
			for _, update := range deploymentConfig.LauncherUpdate {
				addUrlWithDetails(urlMap, update.BundleInfoURL, checkDetails{reasonUpdate, operatingsystem, arch, 0})
			}
			for _, update := range deploymentConfig.Bundles {
				addUrlWithDetails(urlMap, update.BundleInfoURL, checkDetails{reasonBundle, operatingsystem, arch, 0})
			}
			for _, command := range deploymentConfig.Execution.Commands {
				report := collectCommandURLs(urlMap, deploymentConfig, operatingsystem, arch, command, skipJarCheck)
				if report != nil {
					reps = append(reps, report)
				}
			}
		}
	}
	return urlMap, reps
}

func addUrlWithDetails(urlMap map[string]checkDetails, url string, details checkDetails) {
	presentDetails, ok := urlMap[url]
	if ok {
		presentDetails.othersCount++
		urlMap[url] = presentDetails
	} else {
		urlMap[url] = details
	}
}

func collectCommandURLs(urlMap map[string]checkDetails, deploymentConfig *config.DeploymentConfig, os, arch string, command config.Command, skipJarCheck bool) *report {
	commandNameUnix := strings.ReplaceAll(command.Name, `\`, "/")
	if path.IsAbs(commandNameUnix) || !strings.Contains(commandNameUnix, "/") {
		return errorReport("Path '%s' is not a relative path which descends into at least one folder.", commandNameUnix)
	}
	bundleName := misc.FirstElementOfPath(commandNameUnix)
	bundleURL := getBundleURL(bundleName, deploymentConfig)
	if bundleURL == "" {
		return errorReport("Could not get bundle URL for bundle \"%s\" for platform %s-%s. (Required for command \"%s\")", bundleName, os, arch, command.Name)
	}
	binaryURL := misc.MustJoinURL(bundleURL, stripFirstPathElement(commandNameUnix))
	if os == system.OsWindows && !strings.HasSuffix(binaryURL, ".exe") {
		binaryURL += ".exe"
	}
	addUrlWithDetails(urlMap, binaryURL, checkDetails{reasonCommand, os, arch, 0})
	if !skipJarCheck {
		if strings.HasSuffix(binaryURL, "/java.exe") || strings.HasSuffix(binaryURL, "/javaw.exe") ||
			strings.HasSuffix(binaryURL, "/java") {
			err := collectJarURL(urlMap, deploymentConfig, command, os, arch)
			if err != nil {
				return errorReport("Could not get JAR URL for bundle \"%s\" for platform %s-%s (Required for command \"%s\"): %v", bundleName, os, arch, command.Name, err)
			}
		}
	}
	return nil
}

func stripFirstPathElement(s string) string {
	s = path.Clean(s)
	parts := strings.Split(s, "/")
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts[1:], "/")
}

func collectJarURL(urlMap map[string]checkDetails, deploymentConfig *config.DeploymentConfig, command config.Command, os, arch string) error {
	check := false
	for _, arg := range command.Arguments {
		if check {
			jarPath := strings.ReplaceAll(arg, `\`, "/")
			bundleName := misc.FirstElementOfPath(jarPath)
			bundleURL := getBundleURL(bundleName, deploymentConfig)
			if bundleURL == "" {
				return fmt.Errorf("JAR path '%s' does not descend into a bundle directory", arg)
			}
			jarURL := misc.MustJoinURL(bundleURL, stripFirstPathElement(jarPath))
			addUrlWithDetails(urlMap, jarURL, checkDetails{reasonJar, os, arch, 0})
			break
		}
		if arg == "-jar" {
			check = true
		}
	}
	return nil
}

func getBundleURL(bundleName string, deploymentConfig *config.DeploymentConfig) string {
	for _, bundle := range deploymentConfig.Bundles {
		if bundle.LocalDirectory == bundleName {
			return bundle.BaseURL
		}
	}
	return ""
}

func fatalf(formatMessage string, args ...interface{}) {
	fmt.Printf("\033[0;91mFatal: "+formatMessage+"\033[0m\n", args...)
	os.Exit(1)
}
