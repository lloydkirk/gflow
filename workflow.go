package main

import (
	"encoding/json"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/ghodss/yaml"
)

const (
	// ExitJobsFailed indicates that one or more jobs failed
	ExitJobsFailed int = 1 + iota
)

// The Workflow type abstracts the entrypoint of a given workflow
// It manages jobs, both launching them and waiting for them to finish
// When jobs fail, it infers the errors and returns a nonzero exit status
// A Workflow dir will contain logs, scripts, and the PATH of the process
type Workflow struct {
	WorkflowDir string `json:"workflow_dir"`
	logDir      string
	execDir     string
	tmpDir      string
	wfJSONPath  string
	eventDBPath string

	Jobs []*Job `json:"jobs"`

	currentJobID int
	jobIDLock    *sync.Mutex
	failedJobs   *failedJobs
}

// func (w *Workflow) InitFlags() {
// 	// TODO: add flag parsing here
// }

func (w *Workflow) initWorkflow() {
	w.createWorkflowDirs()
}

// AddJob adds a job or list of jobs to a workflow
func (w *Workflow) AddJob(j ...*Job) {
	w.Jobs = append(w.Jobs, j...)
}

func (w *Workflow) pathToWfDir(s ...string) string {
	return filepath.Join(append([]string{w.WorkflowDir}, s...)...)
}

func (w *Workflow) createWorkflowDirs() {
	// TODO: where do responsibilities stop?
	// _, err := os.Stat(w.WorkflowDir)
	// if err != nil {
	// 	if os.IsNotExist(err) {
	// 		err = os.Mkdir(w.WorkflowDir, 0775)
	// 		if err != nil {
	// 			log.Fatal(err)
	// 		}
	// 		return
	// 	}
	// 	log.Fatal(err)
	// }

	for _, d := range []string{w.WorkflowDir, w.execDir, w.logDir} {
		err := os.MkdirAll(d, 0755)
		if err != nil {
			log.Fatal(err)
		}
	}
}

func (w *Workflow) incrementCurrentJobID() int {
	w.jobIDLock.Lock()
	w.currentJobID++
	w.jobIDLock.Unlock()
	return w.currentJobID
}

func newWorkflow(wfDir string) *Workflow {
	absWfDir, err := filepath.Abs(wfDir)
	if err != nil {
		log.Fatal(err)
	}
	logDir := filepath.Join(absWfDir, ".gflow", "log")
	execDir := filepath.Join(absWfDir, ".gflow", "exec")
	tmpDir := filepath.Join(absWfDir, ".gflow", "tmp")
	wfJSONPath := filepath.Join(absWfDir, ".gflow", "wf.json")
	eventDBPath := filepath.Join(absWfDir, ".gflow", "event.db")

	wf := &Workflow{
		absWfDir, logDir, execDir, tmpDir, wfJSONPath, eventDBPath,
		[]*Job{}, 0, &sync.Mutex{}, newFailedJobs(),
	}
	wf.createWorkflowDirs()
	return wf
}

func (w *Workflow) inferExitStatus() int {
	numberFailedJobs := len(w.failedJobs.jobs)
	if numberFailedJobs > 0 {
		log.Println("Error:", strconv.Itoa(numberFailedJobs), "jobs failed")
		return ExitJobsFailed
	}
	return 0
}

func (w *Workflow) writeWorkflowJSON() {
	f, err := os.Create(w.wfJSONPath)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if enc.Encode(w); err != nil {
		log.Fatal(err)
	}
}

// Run runs the workflow, which has a dependency tree of jobs
// Run initializes each job in order of dependency, then executes
// each job until everything returns. The exit status is then inferred,
// and the workflow JSON file is written to the filesystem.
func (w *Workflow) Run() int {
	w.initWorkflow()
	wg := &sync.WaitGroup{}

	db, err := setupEventDB(w.eventDBPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	for _, j := range w.Jobs {
		err := j.initJob()
		if err != nil {
			log.Fatalf("Failed initializing job_id: %d", j.ID)
		}
		wg.Add(1)
		go j.runJob(wg, db)
	}

	wg.Wait()
	w.writeWorkflowJSON()
	exitStatus := w.inferExitStatus()
	if exitStatus != 0 {
		log.Printf("Workflow failed: exit status: %d", exitStatus)
		return exitStatus
	}
	log.Printf("Workflow success")
	return exitStatus
}

func newJobFromJob(w *Workflow, j *Job, deps []*Job) *Job {
	return newJob(w, j.Directories, deps, j.Outputs, j.CleanTmp, j.Cmd)
}

func workflowFromYaml(yamlBytes []byte) *Workflow {
	var yw Workflow
	err := yaml.Unmarshal(yamlBytes, &yw)
	if err != nil {
		log.Fatalf("Error unmarshalling workflow: %v\n", err)
	}
	w := newWorkflow(yw.WorkflowDir)
	jobs := []*Job{}
	for _, job := range yw.Jobs {
		deps := []*Job{}
		for _, depJob := range job.Dependencies {
			deps = append(jobs, newJobFromJob(w, depJob, deps))
		}
		jobs = append(jobs, newJobFromJob(w, job, deps))
	}
	w.Jobs = jobs
	return w
}

func runFromYaml(yamlPath string) int {
	yamlBytes, err := ioutil.ReadFile(yamlPath)
	if err != nil {
		log.Fatalf("Error reading workflow yaml: %v\n", err)
	}
	w := workflowFromYaml(yamlBytes)
	return w.Run()
}
