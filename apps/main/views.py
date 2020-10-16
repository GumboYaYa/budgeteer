from django.shortcuts import render
from .models import Transaction


def list_view(request):
    context = {}
    context["dataset"] = Transaction.objects.all()

    return render(request, "main/index.html", context)
