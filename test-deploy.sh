#!/bin/bash

set -e

TMPDIR=$(mktemp -d)

echo "<html><head><link rel='stylesheet' href='./styles.css'/></head><body><h1>It works!</h1></body></html>" > $TMPDIR/index.html
echo "h1 { color: red; }" > $TMPDIR/styles.css
mkdir $TMPDIR/protected
echo "<html><head></head><body><h1>HTML in dir!</h1></body></html>" > $TMPDIR/protected/index.html
tar -czvf content.tar.gz -C $TMPDIR .

# deploy to __ROOT__
curl --data-binary @content.tar.gz "http://forge-pages.test:8080/deploy?repo=testuser/proj1"
echo

# deploy to protected dir
curl --data-binary @content.tar.gz "http://forge-pages.test:8080/deploy?repo=testuser/proj1&additional_base_path=protected&protect=1"
echo

rm -rf $TMPDIR content.tar.gz
