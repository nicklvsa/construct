package main

import (
	"os"

	"github.com/nicklvsa/construct/pkg"
	flag "github.com/spf13/pflag"
)

type ConstructInput struct {
	FileName string
	Commands []string
}

func handleArgs() *ConstructInput {
	defaultFileName := "Constfile"

	args := os.Args[1:]
	if len(args) <= 0 {
		return &ConstructInput{
			FileName: defaultFileName,
		}
	}

	info, err := os.Stat(args[0])
	if err != nil {
		return &ConstructInput{
			FileName: defaultFileName,
			Commands: args,
		}
	}

	if info.IsDir() {
		return &ConstructInput{
			FileName: defaultFileName,
			Commands: args,
		}
	}

	return &ConstructInput{
		FileName: args[0],
		Commands: args[1:],
	}
}

func main() {
	inputs := handleArgs()

	p := pkg.NewParser(inputs.FileName)
	data, err := p.Parse()
	if err != nil {
		panic(err)
	}

	var concurrency int
	flag.IntVar(&concurrency, "concurrency", 1, "")

	flag.Parse()

	executor := pkg.NewExecutor(data, concurrency)
	if err := executor.Exec(inputs.Commands); err != nil {
		panic(err)
	}
}
