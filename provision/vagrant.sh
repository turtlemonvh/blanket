#!/bin/bash

############
# Set up basic deps
############

apt-get update
apt-get install -y tree build-essential git htop dstat

############
# Setup Go
############

# https://github.com/pecke01/go_vagrant/blob/master/Vagrantfile

export VAGRANT_HOME=/home/vagrant

if [ ! -e /usr/local/go/bin ]; then
    cd /tmp

    GO_VERSION=1.6
    echo 'Downloading go$GO_VERSION.linux-amd64.tar.gz'
    wget -q https://storage.googleapis.com/golang/go$GO_VERSION.linux-amd64.tar.gz

    echo 'Unpacking go language'
    tar -C /usr/local -xzf go$GO_VERSION.linux-amd64.tar.gz

    BASHRC=$VAGRANT_HOME/.bashrc
    if ! grep -q GOPATH $BASHRC; then
        mkdir -p /opt/gopath
        chown -R vagrant:vagrant /opt/gopath/
        echo 'Setting up correct env. variables'
        echo 'export PATH=$PATH:/usr/local/go/bin' >> $BASHRC
        echo 'export GOPATH=/opt/gopath/' >> $BASHRC
        echo 'export PATH=$PATH:$GOPATH/bin' >> $BASHRC
    fi

    source $BASHRC

fi

############
# Set up project
############

mkdir -p $GOPATH/src/github.com/turtlemonvh/
ln -s $VAGRANT_HOME/blanket $GOPATH/src/github.com/turtlemonvh/blanket
cd $GOPATH/src/github.com/turtlemonvh/blanket
go get ./...

