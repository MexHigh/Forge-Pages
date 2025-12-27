#!/bin/bash

curl -X DELETE "http://forge-pages.test:8080/deploy?repo=testuser/proj1"
echo

curl -X DELETE "http://forge-pages.test:8080/deploy?repo=testuser/proj1&additional_base_path=protected"
echo