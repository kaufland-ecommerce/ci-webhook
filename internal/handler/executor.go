package handler

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/hashicorp/go-multierror"

	"github.com/kaufland-ecommerce/ci-webhook/internal/hook"
)

type Executor struct {
	hook   *hook.Hook
	req    *hook.Request
	logger *slog.Logger

	files []hook.FileParameter
}

func NewExecutor(h *hook.Hook, req *hook.Request, logger *slog.Logger) *Executor {
	return &Executor{
		hook:   h,
		req:    req,
		logger: logger,
	}
}

func (e *Executor) checkCommandExistsAndValid() (string, error) {
	var path string
	command := e.hook.ExecuteCommand
	if filepath.IsAbs(command) || e.hook.CommandWorkingDirectory == "" {
		path = command
	} else {
		path = filepath.Join(e.hook.CommandWorkingDirectory, command)
	}

	cmdPath, err := exec.LookPath(path)
	if err != nil {
		e.logger.Error("error looking up command", "error", err)
		// check if parameters specified in execute-command by mistake
		if strings.IndexByte(command, ' ') != -1 {
			s := strings.Fields(command)[0]
			e.logger.Warn(fmt.Sprintf("use 'pass-arguments-to-command' to specify args for '%s'", s))
		}
		return "", err
	}
	return cmdPath, nil
}

func (e *Executor) prepareFileArguments() ([]string, error) {
	var result *multierror.Error

	files, err := e.hook.ExtractCommandArgumentsForFile(e.req)
	result = multierror.Append(result, err)
	e.logger.Error("error extracting command arguments for file", "error", err)
	var envs []string
	for i := range files {
		tmpfile, err := os.CreateTemp(e.hook.CommandWorkingDirectory, files[i].EnvName)
		flog := e.logger.With("var", files[i].EnvName, "file_name", tmpfile.Name())
		if err != nil {
			result = multierror.Append(result, fmt.Errorf("error creating temp file [%w]", err))
			continue
		}
		flog.Info("writing file argument contents to file")
		if _, err := tmpfile.Write(files[i].Data); err != nil {
			result = multierror.Append(result, err)
			flog.Error("error writing file", "error", err)
			continue
		}
		if err := tmpfile.Close(); err != nil {
			result = multierror.Append(result, err)
			flog.Error("error closing file", "error", err)
			continue
		}

		files[i].File = tmpfile
		envs = append(envs, fmt.Sprintf("%s=%s", files[i].EnvName, tmpfile.Name()))
	}
	e.files = files
	return envs, result.ErrorOrNil()
}

func (e *Executor) cleanupFileArguments() {
	for _, file := range e.files {
		if file.File != nil {
			e.logger.Info("removing file", "file_name", file.File.Name())
			if err := os.Remove(file.File.Name()); err != nil {
				e.logger.Error("error removing file", "error", err, "file_name", file.File.Name())
			}
		}
	}
}

func (e *Executor) execHookCommand(w io.Writer) error {
	// check the command exists
	cmdPath, err := e.checkCommandExistsAndValid()
	if err != nil {
		return err
	}
	// construct command
	cmd := exec.Command(cmdPath)
	cmd.Dir = e.hook.CommandWorkingDirectory
	// arguments
	cmd.Args, err = e.hook.ExtractCommandArguments(e.req)
	if err != nil {
		e.logger.Error("error extracting command arguments", "error", err)
	}
	// environment variables
	var envs []string
	envs, err = e.hook.ExtractCommandArgumentsForEnv(e.req)
	e.logger.Error("error extracting command arguments for environment", "error", err)
	// file-based environment variables
	envFileArgs, err := e.prepareFileArguments()
	defer e.cleanupFileArguments()
	if err != nil {
		e.logger.Error("error preparing file arguments", "error", err)
	}
	envs = append(envs, envFileArgs...)
	// set all on command
	cmd.Env = append(os.Environ(), envs...)
	e.logger.Info("executing command",
		"command", cmd.Path,
		"arguments", cmd.Args,
		"environment", cmd.Env,
		"working_directory", cmd.Dir,
	)

	log.Printf("[%s] executing %s (%s) with arguments %q and environment %s using %s as cwd\n",
		e.req.ID, e.hook.ExecuteCommand, cmd.Path, cmd.Args, envs, cmd.Dir)

	cmd.Stderr = w
	cmd.Stdout = w

	err = cmd.Run()
	if err != nil {
		log.Printf("[%s] error occurred: %+v\n", e.req.ID, err)
	}
	log.Printf("[%s] finished handling %s\n", e.req.ID, e.hook.ID)
	return err
}

func (e *Executor) Execute(w io.Writer) error {
	commandOutputBuf := &bytes.Buffer{}
	mw := io.MultiWriter(w, commandOutputBuf)
	err := e.execHookCommand(mw)
	e.logger.Info("command output: " + commandOutputBuf.String())
	return err
}
