package singleflightx

import (
	"fmt"
)

func ExampleGroup() {
	g := new(Group[string, int])

	block := make(chan struct{})
	res1c := g.DoChanX([]string{"foo", "bar"}, func(keys []string) (map[string]int, error) {
		<-block
		return map[string]int{"foo": 21, "bar": 42}, nil
	})
	res2c := g.DoChanX([]string{"foo", "bar"}, func(keys []string) (map[string]int, error) {
		<-block
		return map[string]int{"foo": 21, "bar": 42}, nil
	})
	close(block)

	res1foo := <-res1c["foo"]
	res1bar := <-res1c["bar"]

	res2foo := <-res2c["foo"]
	res2bar := <-res2c["bar"]

	// Results are shared by functions executed with duplicate keys.
	fmt.Println("Shared:", res2foo.Shared)
	// Only the first function is executed: it is registered and started with "key",
	// and doesn't complete before the second funtion is registered with a duplicate key.
	fmt.Println("Equal results:", res1bar.Value.Value == res2bar.Value.Value)
	fmt.Println("Result:", res1foo.Value.Value)

	// Output:
	// Shared: true
	// Equal results: true
	// Result: 21
}
