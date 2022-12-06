#!/bin/sh

while [ $# -ne 0 ]
do
    echo "Converting json to ABI $1 > ${1%.json}Striped.json"
	cat "$1" | jq '.abi' > ${1%.json}Striped.json
	shift
done
