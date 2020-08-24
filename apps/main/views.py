from django.shortcuts import render
from django.views.generic import ListView
from .models import Transaction

class Overview(ListView):
    model = Transaction
    template_name = "main/overview.html"
    context_object_name = "imported_data"
