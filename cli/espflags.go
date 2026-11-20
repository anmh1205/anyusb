package main

import "flag"

type espFlags struct {
	flags     *flag.FlagSet
	baud      *int
	port, idf *string
	dtr, rts  *bool
}

func parseESPFlags(command string) espFlags {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	return espFlags{flags,
		flags.Int("baud", 115200, "serial baud rate"),
		flags.String("port", "", "host serial port (default: discover)"),
		flags.String("idf", "", "ESP-IDF flasher_args.json"),
		flags.Bool("dtr", false, "set DTR on monitor startup"),
		flags.Bool("rts", false, "set RTS on monitor startup"),
	}
}
