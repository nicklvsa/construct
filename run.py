import sys 
import requests 
print(sys.argv[1:]) 
print(requests.get('https://dummyjson.com/products/1').json()) 
