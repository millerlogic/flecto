# flecto
Restricted file system (experimental) for Linux

Flecto is an experimental project for restricting access to the file system.
It currently contains two fs types: ```userfs``` and ```subprocfs```

* ```userfs``` points to an existing directory, and anything accessed through the mount will ask the user for permission.

    ```flecto-fuse userfs /home/realhome /home/newhome```

    ![cat stuff.txt](/../pics/userfscat.png?raw=true)
    
    ![Allow read stuff.txt?](/../pics/userfsnotify.png?raw=true)

* ```subprocfs``` wraps ```/proc``` and limits its visibility into the system.

    ```flecto-fuse subprocfs /newproc```

