//go:build generator
/*
	Timelinize
	Copyright (c) 2024 Sergio Rubio

	This program is free software: you can redistribute it and/or modify
	it under the terms of the GNU Affero General Public License as published
	by the Free Software Foundation, either version 3 of the License, or
	(at your option) any later version.

	This program is distributed in the hope that it will be useful,
	but WITHOUT ANY WARRANTY; without even the implied warranty of
	MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
	GNU Affero General Public License for more details.

	You should have received a copy of the GNU Affero General Public License
	along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/
package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/dave/jennifer/jen"
)

func main() {
	source := jen.NewFile("main")
	source.HeaderComment("//go:generate go run generator.go")

	list := datasources()
	if len(list) == 0 {
		return
	}

	for _, ds := range list {
		source.Anon(ds)
	}

	f, err := os.Create("datasources.go")
	if err != nil {
		genFailed(err)
	}
	defer f.Close()

	fmt.Fprintf(f, "%#v", source)
}

func datasources() []string {
	var datasources []string

	dsFile := ".localds"
	if _, err := os.Stat(dsFile); os.IsNotExist(err) {
		dsFile = ".datasources"
	}

	f, err := os.Open(dsFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "** no .datasources file found\n")
			return datasources
		}
		genFailed(err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Split(bufio.ScanLines)

	for scanner.Scan() {
		ds := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(ds, "//") || ds == "" {
			continue
		}

		// local data sources
		if !strings.Contains(ds, "/") {
			ds = "github.com/timelinize/timelinize/datasources/" + ds
		}

		datasources = append(datasources, ds)
	}

	return datasources
}

func genFailed(err error) {
	fmt.Fprintf(os.Stderr, "generating datasources.go failed: %s", err)
	os.Exit(1)
}
